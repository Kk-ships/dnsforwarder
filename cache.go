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
	if answers, ok := loadFromCache(key); ok {
		atomic.AddInt64(&cacheHits, 1)
		return answers // No copy, just return the slice reference
	}
	answers := resolver(domain, qtype)
	go func(ans []dns.RR, k string) {
		saveToCache(k, ans, defaultDNSCacheTTL)
	}(answers, key)
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
