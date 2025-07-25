package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"dnsloadbalancer/config"
	"dnsloadbalancer/dnsresolver"
	"dnsloadbalancer/domainrouting"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/metric"

	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
)

var (
	cacheHits           int64
	cacheRequests       int64
	DnsCache            *cache.Cache
	DefaultDNSCacheTTL  time.Duration = 30 * time.Minute
	EnableMetrics       bool
	EnableClientRouting bool
	EnableDomainRouting bool
	persistenceTicker   *time.Ticker
)

// CacheEntry represents a cache entry for persistence
type CacheEntry struct {
	Key        string    `json:"key"`
	Answers    []string  `json:"answers"`    // DNS RR serialized as strings
	Expiration time.Time `json:"expiration"` // Zero time means no expiration
}

// CacheSnapshot represents the entire cache state for persistence
type CacheSnapshot struct {
	Entries   []CacheEntry `json:"entries"`
	Timestamp time.Time    `json:"timestamp"`
	Stats     CacheStats   `json:"stats"`
}

// CacheStats represents cache statistics
type CacheStats struct {
	TotalHits     int64 `json:"total_hits"`
	TotalRequests int64 `json:"total_requests"`
}

func Init(defaultDNSCacheTTL time.Duration, enableMetrics bool, _ interface{}, enableClientRouting bool, enableDomainRouting bool) {
	DnsCache = cache.New(defaultDNSCacheTTL, 2*defaultDNSCacheTTL)
	DefaultDNSCacheTTL = defaultDNSCacheTTL
	EnableMetrics = enableMetrics
	EnableClientRouting = enableClientRouting
	EnableDomainRouting = enableDomainRouting
	// Load cache from disk if persistence is enabled
	if err := LoadCacheFromFile(); err != nil {
		logutil.Logger.Errorf("Failed to load cache from file: %v", err)
	}
	// Start periodic cache persistence
	StartCachePersistence()
}

func CacheKey(domain string, qtype uint16) string {
	var b strings.Builder
	b.Grow(len(domain) + 1 + 5)
	b.WriteString(domain)
	b.WriteByte(':')
	b.WriteString(strconv.FormatUint(uint64(qtype), 10))
	return b.String()
}

func SaveToCache(key string, answers []dns.RR, ttl time.Duration) {
	DnsCache.Set(key, answers, ttl)
}

func LoadFromCache(key string) ([]dns.RR, bool) {
	val, found := DnsCache.Get(key)
	if !found {
		return nil, false
	}
	answers, ok := val.([]dns.RR)
	if !ok {
		return nil, false
	}
	return answers, true
}

func ResolverWithCache(domain string, qtype uint16, clientIP string) []dns.RR {
	start := time.Now()
	key := CacheKey(domain, qtype)
	atomic.AddInt64(&cacheRequests, 1)
	qTypeStr := dns.TypeToString[qtype]
	if answers, ok := LoadFromCache(key); ok {
		atomic.AddInt64(&cacheHits, 1)
		if EnableMetrics {
			metric.MetricsRecorderInstance.RecordCacheHit()
			metric.MetricsRecorderInstance.RecordDNSQuery(qTypeStr, "cached", time.Since(start))
		}
		return answers
	}
	if EnableMetrics {
		metric.MetricsRecorderInstance.RecordCacheMiss()
	}
	var answers []dns.RR
	var resolver func(string, uint16, string) []dns.RR
	resolver = dnsresolver.ResolverForClient
	if EnableDomainRouting {
		if _, ok := domainrouting.RoutingTable[domain]; ok {
			resolver = dnsresolver.ResolverForDomain
		}
	}
	answers = resolver(domain, qtype, clientIP)
	status := "success"
	if len(answers) == 0 {
		status = "nxdomain"
		go SaveToCache(key, answers, DefaultDNSCacheTTL/config.NegativeResponseTTLDivisor)
		if EnableMetrics {
			metric.MetricsRecorderInstance.RecordDNSQuery(qTypeStr, status, time.Since(start))
		}
		return answers
	}

	minTTL := uint32(^uint32(0))
	useDefaultTTL := false

	for i := range answers {
		rr := answers[i]
		ttl := rr.Header().Ttl
		if ttl < minTTL {
			minTTL = ttl
		}
		switch v := rr.(type) {
		case *dns.A:
			if v.A.String() == config.ARecordInvalidAnswer {
				useDefaultTTL = true
			}
		case *dns.AAAA:
			if v.AAAA.String() == config.AAAARecordInvalidAnswer {
				useDefaultTTL = true
			}
		}
		if useDefaultTTL {
			break
		}
	}
	ttl := DefaultDNSCacheTTL
	if !useDefaultTTL && minTTL > 0 {
		ttlDuration := time.Duration(minTTL) * time.Second
		if ttlDuration < config.MinCacheTTL {
			ttl = config.MinCacheTTL
		} else if ttlDuration > config.MaxCacheTTL {
			ttl = config.MaxCacheTTL
		} else {
			ttl = ttlDuration
		}
	}
	go SaveToCache(key, answers, ttl)
	if EnableMetrics {
		metric.MetricsRecorderInstance.RecordDNSQuery(qTypeStr, status, time.Since(start))
	}
	return answers
}

// ensureCacheDir ensures the cache persistence directory exists and is writable
func ensureCacheDir() error {
	if !config.EnableCachePersistence {
		return nil
	}

	dir := filepath.Dir(config.CachePersistenceFile)

	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		// Try to create the directory
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create cache directory %s: %w", dir, err)
		}
		logutil.Logger.Infof("Created cache persistence directory: %s", dir)
	}

	// Test write permissions by creating a temporary file
	testFile := filepath.Join(dir, ".cache_test")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		// Try alternative locations if the primary one fails
		altPaths := []string{
			"/tmp/dns_cache.json",
			"./dns_cache.json",
			"./cache/dns_cache.json",
		}

		for _, altPath := range altPaths {
			altDir := filepath.Dir(altPath)
			if err := os.MkdirAll(altDir, 0755); err != nil {
				continue
			}

			testAltFile := filepath.Join(altDir, ".cache_test")
			if err := os.WriteFile(testAltFile, []byte("test"), 0644); err != nil {
				continue
			}

			// Clean up test file
			err = os.Remove(testAltFile)
			if err != nil {
				logutil.Logger.Warnf("Failed to remove test file %s: %v", testAltFile, err)
			}

			// Update the config to use the working path
			config.CachePersistenceFile = altPath
			logutil.Logger.Warnf("Using alternative cache persistence path: %s", altPath)
			return nil
		}

		return fmt.Errorf("cache directory %s is not writable and no alternative paths work: %w", dir, err)
	}

	// Clean up test file
	if err := os.Remove(testFile); err != nil {
		logutil.Logger.Warnf("Failed to remove test file %s: %v", testFile, err)
	}

	return nil
}

// SaveCacheToFile persists the current cache state to disk
func SaveCacheToFile() error {
	if !config.EnableCachePersistence || DnsCache == nil {
		return nil
	}

	// Ensure cache directory exists and is writable
	if err := ensureCacheDir(); err != nil {
		return fmt.Errorf("cache directory check failed: %w", err)
	}

	logutil.Logger.Debug("Starting cache persistence to disk")

	items := DnsCache.Items()
	entries := make([]CacheEntry, 0, len(items))

	// Capture current time once to avoid repeated time.Now() calls
	now := time.Now()

	for key, item := range items {
		// Check if the item has expired
		if item.Expiration > 0 && now.After(time.Unix(0, item.Expiration)) {
			continue // Skip expired items
		}

		answers, ok := item.Object.([]dns.RR)
		if !ok {
			logutil.Logger.Warnf("Failed to cast cache item for key %s", key)
			continue
		}

		// Serialize DNS RRs to strings
		answerStrings := make([]string, len(answers))
		for i, rr := range answers {
			answerStrings[i] = rr.String()
		}

		var expiration time.Time
		if item.Expiration > 0 {
			expiration = time.Unix(0, item.Expiration)
		}
		// Note: Zero expiration time means the item never expires

		entries = append(entries, CacheEntry{
			Key:        key,
			Answers:    answerStrings,
			Expiration: expiration,
		})
	}

	snapshot := CacheSnapshot{
		Entries:   entries,
		Timestamp: time.Now(),
		Stats: CacheStats{
			TotalHits:     atomic.LoadInt64(&cacheHits),
			TotalRequests: atomic.LoadInt64(&cacheRequests),
		},
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("failed to marshal cache data: %w", err)
	}

	err = os.WriteFile(config.CachePersistenceFile, data, 0644)
	if err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	logutil.Logger.Debugf("Cache persisted to disk: %d entries saved to %s", len(entries),
		config.CachePersistenceFile)
	return nil
}

// LoadCacheFromFile loads the cache state from disk
func LoadCacheFromFile() error {
	if !config.EnableCachePersistence {
		return nil
	}

	// Ensure cache directory exists and is writable (for future saves)
	if err := ensureCacheDir(); err != nil {
		logutil.Logger.Warnf("Cache directory check failed, but continuing to load: %v", err)
	}

	if _, err := os.Stat(config.CachePersistenceFile); os.IsNotExist(err) {
		logutil.Logger.Info("No cache persistence file found, starting with empty cache")
		return nil
	}

	logutil.Logger.Debug("Loading cache from disk")

	data, err := os.ReadFile(config.CachePersistenceFile)
	if err != nil {
		return fmt.Errorf("failed to read cache file: %w", err) // Return error if file read fails
	}

	var snapshot CacheSnapshot
	err = json.Unmarshal(data, &snapshot)
	if err != nil {
		return fmt.Errorf("failed to unmarshal cache data: %w", err)
	}
	// Check if the cache file is too old
	if time.Since(snapshot.Timestamp) > config.CachePersistenceMaxAge {
		logutil.Logger.Warnf("Cache persistence file is too old (%v > %v), starting with empty cache",
			time.Since(snapshot.Timestamp), config.CachePersistenceMaxAge)
		return nil
	}
	loadedCount := 0

	// Capture current time once to avoid repeated time.Now() calls
	now := time.Now()

	for _, entry := range snapshot.Entries {
		// Skip expired entries
		if !entry.Expiration.IsZero() && now.After(entry.Expiration) {
			continue
		}

		// Parse DNS RRs from strings
		answers := make([]dns.RR, 0, len(entry.Answers))
		for _, answerStr := range entry.Answers {
			rr, err := dns.NewRR(answerStr)
			if err != nil {
				logutil.Logger.Warnf("Failed to parse DNS RR '%s': %v", answerStr, err)
				continue
			}
			answers = append(answers, rr)
		}

		if len(answers) > 0 {
			// Calculate remaining TTL
			var ttl time.Duration
			if entry.Expiration.IsZero() {
				// Zero expiration means no expiration, use default TTL
				ttl = DefaultDNSCacheTTL
			} else {
				ttl = time.Until(entry.Expiration)
				if ttl <= 0 {
					continue // Skip if already expired
				}
			}

			DnsCache.Set(entry.Key, answers, ttl)
			loadedCount++
		}
	}

	// Restore statistics
	atomic.StoreInt64(&cacheHits, snapshot.Stats.TotalHits)
	atomic.StoreInt64(&cacheRequests, snapshot.Stats.TotalRequests)

	logutil.Logger.Infof("Cache loaded from disk: %d entries restored from %s", loadedCount, config.CachePersistenceFile)
	return nil
}

// StartCachePersistence starts the periodic cache persistence
func StartCachePersistence() {
	if !config.EnableCachePersistence {
		return
	}

	// Test cache persistence once during startup
	if err := ensureCacheDir(); err != nil {
		logutil.Logger.Errorf("Cache persistence disabled due to directory issues: %v", err)
		return
	}

	persistenceTicker = time.NewTicker(config.CachePersistenceInterval)
	go func() {
		defer persistenceTicker.Stop()
		for range persistenceTicker.C {
			if err := SaveCacheToFile(); err != nil {
				logutil.Logger.Errorf("Failed to persist cache: %v", err)
			}
		}
	}()

	logutil.Logger.Infof("Cache persistence enabled with interval: %v", config.CachePersistenceInterval)
}

// StopCachePersistence stops the periodic cache persistence and saves one final time
func StopCachePersistence() {
	if persistenceTicker != nil {
		persistenceTicker.Stop()
	}

	if config.EnableCachePersistence {
		if err := SaveCacheToFile(); err != nil {
			logutil.Logger.Errorf("Failed to save cache during shutdown: %v", err)
		} else {
			logutil.Logger.Info("Cache saved successfully during shutdown")
		}
	}
}

func StartCacheStatsLogger() {
	ticker := time.NewTicker(config.DefaultDNSStatslog)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			hits := atomic.LoadInt64(&cacheHits)
			requests := atomic.LoadInt64(&cacheRequests)
			hitPct := 0.0
			if requests > 0 {
				hitPct = (float64(hits) / float64(requests)) * 100
			}
			logutil.Logger.Infof("Requests: %d, Hits: %d, Hit Rate: %.2f%%, Miss Rate: %.2f%%",
				requests, hits, hitPct, 100-hitPct)
			if DnsCache != nil {
				items := DnsCache.Items()
				itemCount := len(items)
				if itemCount == 0 {
					logutil.Logger.Warn("No cache entries found")
				} else {
					logutil.Logger.Infof("Cache Entries: %d", itemCount)
					if EnableMetrics {
						metric.MetricsRecorderInstance.UpdateCacheSize(itemCount)
					}
				}
			}
		}
	}()
}
