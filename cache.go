package main

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
)

type cacheEntry struct {
	Answers []dns.RR
}

const aRecordInvalidAnswer = "0.0.0.0"
const aaaRecordInvalidAnswer = "::"
const negativeResponseTTLDivisor = 4

var (
	cacheHits     int64
	cacheRequests int64
	dnsCache      *cache.Cache
)

func init() {
	dnsCache = cache.New(defaultDNSCacheTTL, 2*defaultDNSCacheTTL)
}

// Optimized cache key generation with string pooling
func cacheKey(domain string, qtype uint16) string {
	// Try to get from cache first for frequent lookups
	var b strings.Builder
	b.Grow(len(domain) + 1 + 5) // 1 for ':', 5 for max uint16 digits
	b.WriteString(domain)
	b.WriteByte(':')
	b.WriteString(strconv.FormatUint(uint64(qtype), 10))
	return b.String()
}

func saveToCache(key string, answers []dns.RR, ttl time.Duration) {
	dnsCache.Set(key, answers, ttl)
}

func loadFromCache(key string) ([]dns.RR, bool) {
	val, found := dnsCache.Get(key)
	if !found {
		return nil, false
	}
	answers, ok := val.([]dns.RR)
	if !ok {
		return nil, false
	}
	return answers, true
}

func resolverWithCache(domain string, qtype uint16) []dns.RR {
	start := time.Now()
	key := cacheKey(domain, qtype)
	atomic.AddInt64(&cacheRequests, 1)

	qTypeStr := dns.TypeToString[qtype]

	// Attempt to load from cache
	if answers, ok := loadFromCache(key); ok {
		atomic.AddInt64(&cacheHits, 1)
		if enableMetrics {
			metricsRecorder.RecordCacheHit()
			metricsRecorder.RecordDNSQuery(qTypeStr, "cached", time.Since(start))
		}
		return answers
	}

	// Cache miss - record it
	if enableMetrics {
		metricsRecorder.RecordCacheMiss()
	}

	// Resolve the domain
	answers := resolver(domain, qtype)
	status := "success"
	if len(answers) == 0 {
		status = "nxdomain"
		// Cache negative responses with shorter TTL
		go saveToCache(key, answers, defaultDNSCacheTTL/negativeResponseTTLDivisor)
		if enableMetrics {
			metricsRecorder.RecordDNSQuery(qTypeStr, status, time.Since(start))
		}
		return answers
	}

	// Determine the minimum TTL and check for special cases
	minTTL := uint32(^uint32(0)) // Max uint32 value
	useDefaultTTL := false

	// Use more efficient loop
	for i := range answers {
		rr := answers[i]
		ttl := rr.Header().Ttl
		if ttl < minTTL {
			minTTL = ttl
		}

		// Type-specific optimizations
		switch v := rr.(type) {
		case *dns.A:
			if v.A.String() == aRecordInvalidAnswer {
				useDefaultTTL = true
			}
		case *dns.AAAA:
			if v.AAAA.String() == aaaRecordInvalidAnswer {
				useDefaultTTL = true
			}
		}

		if useDefaultTTL {
			break
		}
	}

	// Set TTL for caching with bounds
	ttl := defaultDNSCacheTTL
	if !useDefaultTTL && minTTL > 0 {
		ttlDuration := time.Duration(minTTL) * time.Second
		// Enforce minimum and maximum TTL bounds
		const minCacheTTL = 30 * time.Second
		const maxCacheTTL = 24 * time.Hour

		if ttlDuration < minCacheTTL {
			ttl = minCacheTTL
		} else if ttlDuration > maxCacheTTL {
			ttl = maxCacheTTL
		} else {
			ttl = ttlDuration
		}
	}

	// Save to cache asynchronously
	go saveToCache(key, answers, ttl)

	if enableMetrics {
		metricsRecorder.RecordDNSQuery(qTypeStr, status, time.Since(start))
	}
	return answers
}

// Periodically log cache hit/miss percentages with batched metrics
func startCacheStatsLogger() {
	ticker := time.NewTicker(1 * time.Minute)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			// Use atomic loads for consistent snapshots
			hits := atomic.LoadInt64(&cacheHits)
			requests := atomic.LoadInt64(&cacheRequests)

			hitPct := 0.0
			if requests > 0 {
				hitPct = (float64(hits) / float64(requests)) * 100
			}

			logWithBufferf("[CACHE STATS] Requests: %d, Hits: %d, Hit Rate: %.2f%%, Miss Rate: %.2f%%",
				requests, hits, hitPct, 100-hitPct)

			// Get cache size efficiently
			if dnsCache != nil {
				items := dnsCache.Items()
				itemCount := len(items)
				if itemCount == 0 {
					logWithBufferf("[CACHE STATS] No cache entries found")
				} else {
					logWithBufferf("[CACHE STATS] Cache Entries: %d", itemCount)

					// Update metrics if enabled
					if enableMetrics {
						metricsRecorder.UpdateCacheSize(itemCount)
					}
				}
			}
		}
	}()
}
