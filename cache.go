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

func saveToCache(key string, entry cacheEntry, ttl time.Duration) {
    dnsCache.Set(key, entry.Answers, ttl)
}

func loadFromCache(key string) (cacheEntry, bool) {
    val, found := dnsCache.Get(key)
    if !found {
        return cacheEntry{}, false
    }
    answers, ok := val.([]dns.RR)
    if !ok {
        return cacheEntry{}, false
    }
    return cacheEntry{Answers: answers}, true
}

func resolverWithCache(domain string, qtype uint16) []dns.RR {
    key := cacheKey(domain, qtype)
    go atomic.AddInt64(&cacheRequests, 1)
    if entry, ok := loadFromCache(key); ok {
        go atomic.AddInt64(&cacheHits, 1)
        return entry.Answers
    }
    answers := resolver(domain, qtype)
    go func(ans []dns.RR, k string) {
        saveToCache(k, cacheEntry{Answers: ans}, defaultDNSCacheTTL)
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