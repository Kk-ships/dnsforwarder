package main

import (
	"fmt"
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

var (
	cacheHits     int64
	cacheRequests int64
	dnsCache      *cache.Cache
)

func init() {
	dnsCache = cache.New(defaultDNSCacheTTL, 2*defaultDNSCacheTTL)
}

func cacheKey(domain string, qtype uint16) string {
	return fmt.Sprintf("%s:%d", domain, qtype)
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
	key := cacheKey(domain, qtype)
	atomic.AddInt64(&cacheRequests, 1)

	// Attempt to load from cache
	if answers, ok := loadFromCache(key); ok {
		atomic.AddInt64(&cacheHits, 1)
		return answers
	}

	// Resolve the domain
	answers := resolver(domain, qtype)
	if len(answers) == 0 {
		go saveToCache(key, answers, defaultDNSCacheTTL)
		return answers
	}

	// Determine the minimum TTL and check for special cases
	minTTL := uint32(^uint32(0)) // Max uint32 value
	useDefaultTTL := false
	for _, rr := range answers {
		ttl := rr.Header().Ttl
		if ttl < minTTL {
			minTTL = ttl
		}
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

	// Set TTL for caching
	ttl := defaultDNSCacheTTL
	if !useDefaultTTL && minTTL > 0 {
		ttl = time.Duration(minTTL) * time.Second
	}

	// Save to cache asynchronously
	go saveToCache(key, answers, ttl)

	return answers
}

// Periodically log cache hit/miss percentages
func startCacheStatsLogger() {
	ticker := time.NewTicker(1 * time.Minute)
	go func() {
		for range ticker.C {
			hits := cacheHits
			requests := cacheRequests
			hitPct := 0.0
			if requests > 0 {
				hitPct = (float64(hits) / float64(requests)) * 100
			}
			logWithBufferf("[CACHE STATS] Requests: %d, Hits: %d, Hit Rate: %.2f%%, Miss Rate: %.2f%%",
				requests, hits, hitPct, 100-hitPct)
			// log all cache entry stats
			items := dnsCache.Items()
			if len(items) == 0 {
				logWithBufferf("[CACHE STATS] No cache entries found")
				continue
			}
			logWithBufferf("[CACHE STATS] Cache Entries: %d", len(items))
		}
	}()
}
