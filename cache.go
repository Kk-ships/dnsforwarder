package main

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/redis/go-redis/v9"
)

type cacheEntry struct {
	Answers []dns.RR
}

var defaultValKeyServer = getEnvString("VALKEY_SERVER", "valkey:6379")

var (
	cacheStatsMutex sync.Mutex
	cacheHits       int
	cacheRequests   int
	redisClient     = redis.NewClient(&redis.Options{
		Addr: defaultValKeyServer, // Change as needed
	})
)

func cacheKey(domain string, qtype uint16) string {
	return fmt.Sprintf("%s:%d", domain, qtype)
}

func saveToValkey(key string, entry cacheEntry, ttl time.Duration) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(entry); err != nil {
		return err
	}
	return redisClient.Set(context.Background(), key, buf.Bytes(), ttl).Err()
}

func loadFromValkey(key string) (cacheEntry, bool) {
	val, err := redisClient.Get(context.Background(), key).Bytes()
	if err != nil {
		return cacheEntry{}, false
	}
	var entry cacheEntry
	if err := gob.NewDecoder(bytes.NewReader(val)).Decode(&entry); err != nil {
		return cacheEntry{}, false
	}
	return entry, true
}

func resolverWithCache(domain string, qtype uint16) []dns.RR {
	key := cacheKey(domain, qtype)
	go func() {
		cacheStatsMutex.Lock()
		cacheRequests++
		cacheStatsMutex.Unlock()
	}()
	if entry, ok := loadFromValkey(key); ok {
		go func() {
			cacheStatsMutex.Lock()
			cacheHits++
			cacheStatsMutex.Unlock()
		}()
		return entry.Answers
	}
	answers := resolver(domain, qtype)
	if answers != nil {
		saveToValkey(key, cacheEntry{Answers: answers}, defaultDNSCacheTTL)
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
