package main

import (
	"encoding/gob"
	"fmt"
	"os"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/miekg/dns"
)

type cacheEntry struct {
	answers []dns.RR
}

var (
	dnsCache         = lru.NewLRU[string, cacheEntry](defaultCacheSize, nil, defaultDNSCacheTTL)
	cacheStatsMutex  sync.Mutex
	cacheHits        int
	cacheRequests    int
)

const cacheFile = "dns_cache.gob"

// Save cache to disk
func saveCacheToDisk() error {
	f, err := os.Create(cacheFile)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := gob.NewEncoder(f)
	cache := make(map[string]cacheEntry)
	for _, k := range dnsCache.Keys() {
		if v, ok := dnsCache.Get(k); ok {
			cache[k] = v
		}
	}
	return enc.Encode(cache)
}

// Load cache from disk
func loadCacheFromDisk() error {
	f, err := os.Open(cacheFile)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := gob.NewDecoder(f)
	cache := make(map[string]cacheEntry)
	if err := dec.Decode(&cache); err != nil {
		return err
	}
	for k, v := range cache {
		dnsCache.Add(k, v)
	}
	return nil
}

func init() {
	gob.Register(cacheEntry{})
	gob.Register([]dns.RR{})
}

// Resolver with cache and stats
func resolverWithCache(domain string, qtype uint16) []dns.RR {
	cacheKey := fmt.Sprintf("%s:%d", domain, qtype)

	cacheStatsMutex.Lock()
	cacheRequests++
	cacheStatsMutex.Unlock()

	if entry, ok := dnsCache.Get(cacheKey); ok {
		cacheStatsMutex.Lock()
		cacheHits++
		cacheStatsMutex.Unlock()
		return entry.answers
	}
	answers := resolver(domain, qtype)
	if answers != nil {
		dnsCache.Add(cacheKey, cacheEntry{
			answers: answers,
		})
	}
	return answers
}

// Periodically log cache hit/miss percentages
func startCacheStatsLogger() {
	ticker := time.NewTicker(1 * time.Minute)
	go func() {
		for range ticker.C {
			cacheStatsMutex.Lock()
			hits := cacheHits
			requests := cacheRequests
			cacheStatsMutex.Unlock()
			hitPct := 0.0
			if requests > 0 {
				hitPct = (float64(hits) / float64(requests)) * 100
			}
			logWithBufferf("[CACHE STATS] Requests: %d, Hits: %d, Hit Rate: %.2f%%, Miss Rate: %.2f%%",
				requests, hits, hitPct, 100-hitPct)
		}
	}()
}