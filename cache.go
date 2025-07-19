package main

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"os"
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
	redisClient     *redis.Client
)

func init() {
	gob.Register(&dns.A{})
	gob.Register(&dns.AAAA{})
	gob.Register(&dns.CNAME{})
	gob.Register(&dns.MX{})
	gob.Register(&dns.NS{})
	gob.Register(&dns.TXT{})
}
func connectToRedis() {
	redisClient = redis.NewClient(&redis.Options{
		Addr: defaultValKeyServer, // Change as needed
	})
	logWithBufferf("[REDIS] Connecting to Valkey server at %s", defaultValKeyServer)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		logWithBufferf("[REDIS] Error connecting to valkey server: %v", err)
		// Optionally handle the error, for example by exiting:
		os.Exit(1)
	}
	logWithBufferf("[REDIS] Successfully connected to Valkey server at %s", defaultValKeyServer)
}

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
	go func(ans []dns.RR, k string) {
		err := saveToValkey(k, cacheEntry{Answers: ans}, defaultDNSCacheTTL)
		if err != nil {
			logWithBufferf("[CACHE] Error saving to Valkey: %v", err)
		}
	}(answers, key)
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

			// log all cache entry stats
			keys, err := redisClient.Keys(context.Background(), "*").Result()
			if err != nil {
				logWithBufferf("[CACHE STATS] Error fetching keys: %v", err)
				continue
			}
			if len(keys) == 0 {
				logWithBufferf("[CACHE STATS] No cache entries found")
				continue
			}
			logWithBufferf("[CACHE STATS] Cache Entries: %d", len(keys))
		}
	}()
}
