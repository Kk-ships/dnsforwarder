// Package cache provides DNS response caching functionality with persistence
// and performance optimizations including:
// - Object pooling for string builders and DNS RR slices to reduce GC pressure
// - Elimination of unnecessary goroutines for synchronous cache operations
// - Optimized JSON encoding with pre-allocated buffers
// - Reduced time.Now() calls in hot paths
// - Improved type assertion handling with corruption detection
package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	cfg                 = config.Get() // Global config instance
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

// Pre-allocated buffer pool for cache keys to reduce allocations
var keyBuilderPool = sync.Pool{
	New: func() interface{} {
		return &strings.Builder{}
	},
}

// Pool for DNS RR slices to reduce allocations
var dnsRRSlicePool = sync.Pool{
	New: func() interface{} {
		slice := make([]dns.RR, 0, 16) // Pre-allocate common slice size
		return &slice
	},
}

// getDNSRRSlice gets a slice from the pool and resets it
func getDNSRRSlice() []dns.RR {
	slicePtr := dnsRRSlicePool.Get().(*[]dns.RR)
	slice := *slicePtr
	return slice[:0] // Reset length but keep capacity
}

// putDNSRRSlice returns a slice to the pool if it's not too large
func putDNSRRSlice(slice []dns.RR) {
	const maxPoolSliceSize = 256
	if cap(slice) <= maxPoolSliceSize {
		dnsRRSlicePool.Put(&slice)
	}
}

func CacheKey(domain string, qtype uint16) string {
	builder := keyBuilderPool.Get().(*strings.Builder)
	defer func() {
		builder.Reset()
		keyBuilderPool.Put(builder)
	}()

	// Pre-allocate with exact capacity to avoid reallocation
	builder.Grow(len(domain) + 6) // domain + ':' + up to 5 digits for qtype
	builder.WriteString(domain)
	builder.WriteByte(':')
	builder.WriteString(strconv.FormatUint(uint64(qtype), 10))
	return builder.String()
}

func SaveToCache(key string, answers []dns.RR, ttl time.Duration) {
	// Direct cache set for better performance - no goroutine overhead
	// for frequent small operations like individual DNS responses
	DnsCache.Set(key, answers, ttl)
}

func LoadFromCache(key string) ([]dns.RR, bool) {
	val, found := DnsCache.Get(key)
	if !found {
		return nil, false
	}
	// Use type assertion with early return for better performance
	if answers, ok := val.([]dns.RR); ok {
		return answers, true
	}
	// Log only once for debugging, avoid repeated type assertion failures
	logutil.Logger.Warnf("Cache corruption detected for key %s: invalid type", key)
	// Remove corrupted entry to prevent repeated warnings
	DnsCache.Delete(key)
	return nil, false
}

func ResolverWithCache(domain string, qtype uint16, clientIP string) []dns.RR {
	// Use conditional timer to avoid time.Now() overhead when metrics are disabled
	timer := metric.StartCacheTimer(EnableMetrics)
	key := CacheKey(domain, qtype)
	atomic.AddInt64(&cacheRequests, 1)
	qTypeStr := dns.TypeToString[qtype]
	if answers, ok := LoadFromCache(key); ok {
		atomic.AddInt64(&cacheHits, 1)
		if EnableMetrics {
			metric.GetFastMetricsInstance().FastRecordCacheHit()
			metric.GetFastMetricsInstance().FastRecordDNSQuery(qTypeStr, "cached", timer.Elapsed())
			// Record domain hit for cache hits
			metric.GetFastMetricsInstance().FastRecordDomainHit(domain)
		}
		return answers
	}
	if EnableMetrics {
		metric.GetFastMetricsInstance().FastRecordCacheMiss()
	}
	var answers []dns.RR
	var resolver func(string, uint16, string) []dns.RR
	resolver = dnsresolver.ResolverForClient
	if EnableDomainRouting {
		if _, ok := domainrouting.GetRoutingTable()[domain]; ok {
			resolver = dnsresolver.ResolverForDomain
		}
	}
	answers = resolver(domain, qtype, clientIP)
	status := "success"
	if len(answers) == 0 {
		status = "nxdomain"
		// Removed goroutine - cache negative responses directly
		SaveToCache(key, answers, DefaultDNSCacheTTL/config.NegativeResponseTTLDivisor)
		if EnableMetrics {
			metric.GetFastMetricsInstance().FastRecordDNSQuery(qTypeStr, status, timer.Elapsed())
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
	// Removed goroutine - cache positive responses directly for better performance
	SaveToCache(key, answers, ttl)
	if EnableMetrics {
		metric.GetFastMetricsInstance().FastRecordDNSQuery(qTypeStr, status, timer.Elapsed())
	}
	return answers
}

// ensureCacheDir ensures the cache persistence directory exists and is writable
func ensureCacheDir() error {
	if !cfg.EnableCachePersistence {
		return nil
	}
	dir := filepath.Dir(cfg.CachePersistenceFile)
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
			cfg.CachePersistenceFile = altPath
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
	if !cfg.EnableCachePersistence || DnsCache == nil {
		return nil
	}

	// Ensure cache directory exists and is writable
	if err := ensureCacheDir(); err != nil {
		return fmt.Errorf("cache directory check failed: %w", err)
	}

	logutil.Logger.Debug("Starting cache persistence to disk")

	items := DnsCache.Items()
	// Pre-allocate slice with exact capacity to avoid reallocation
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

		// Pre-allocate slice for answer strings to reduce allocations
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
		Timestamp: now, // Use the same timestamp captured earlier
		Stats: CacheStats{
			TotalHits:     atomic.LoadInt64(&cacheHits),
			TotalRequests: atomic.LoadInt64(&cacheRequests),
		},
	}

	// Use a more efficient JSON encoding approach for large caches
	file, err := os.OpenFile(cfg.CachePersistenceFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create cache file: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			logutil.Logger.Errorf("Failed to close cache file: %v", closeErr)
		}
	}()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ") // Keep formatting for readability

	err = encoder.Encode(snapshot)
	if err != nil {
		return fmt.Errorf("failed to encode cache data: %w", err)
	}

	logutil.Logger.Debugf("Cache persisted to disk: %d entries saved to %s", len(entries),
		cfg.CachePersistenceFile)
	return nil
}

// LoadCacheFromFile loads the cache state from disk
func LoadCacheFromFile() error {
	if !cfg.EnableCachePersistence {
		return nil
	}

	// Ensure cache directory exists and is writable (for future saves)
	if err := ensureCacheDir(); err != nil {
		logutil.Logger.Warnf("Cache directory check failed, but continuing to load: %v", err)
	}

	if _, err := os.Stat(cfg.CachePersistenceFile); os.IsNotExist(err) {
		logutil.Logger.Info("No cache persistence file found, starting with empty cache")
		return nil
	}

	logutil.Logger.Debug("Loading cache from disk")

	data, err := os.ReadFile(cfg.CachePersistenceFile)
	if err != nil {
		return fmt.Errorf("failed to read cache file: %w", err) // Return error if file read fails
	}

	var snapshot CacheSnapshot
	err = json.Unmarshal(data, &snapshot)
	if err != nil {
		return fmt.Errorf("failed to unmarshal cache data: %w", err)
	}
	// Check if the cache file is too old
	if time.Since(snapshot.Timestamp) > cfg.CachePersistenceMaxAge {
		logutil.Logger.Warnf("Cache persistence file is too old (%v > %v), starting with empty cache",
			time.Since(snapshot.Timestamp), cfg.CachePersistenceMaxAge)
		return nil
	}
	loadedCount := 0

	// Capture current time once to avoid repeated time.Now() calls
	now := time.Now()

	// Pre-allocate a slice to reduce allocations during DNS RR parsing
	for _, entry := range snapshot.Entries {
		// Skip expired entries
		if !entry.Expiration.IsZero() && now.After(entry.Expiration) {
			continue
		}

		// Use pooled slice to reduce allocations
		answers := getDNSRRSlice()
		successfulParses := 0

		for _, answerStr := range entry.Answers {
			rr, err := dns.NewRR(answerStr)
			if err != nil {
				logutil.Logger.Warnf("Failed to parse DNS RR '%s': %v", answerStr, err)
				continue
			}
			answers = append(answers, rr)
			successfulParses++
		}

		if successfulParses > 0 {
			// Calculate remaining TTL
			var ttl time.Duration
			if entry.Expiration.IsZero() {
				// Zero expiration means no expiration, use default TTL
				ttl = DefaultDNSCacheTTL
			} else {
				ttl = time.Until(entry.Expiration)
				if ttl <= 0 {
					putDNSRRSlice(answers) // Return to pool before continuing
					continue               // Skip if already expired
				}
			}

			// Make a copy since we're storing it in cache
			cachedAnswers := make([]dns.RR, len(answers))
			copy(cachedAnswers, answers)
			DnsCache.Set(entry.Key, cachedAnswers, ttl)
			loadedCount++
		}

		// Return slice to pool for reuse
		putDNSRRSlice(answers)
	}

	// Restore statistics
	atomic.StoreInt64(&cacheHits, snapshot.Stats.TotalHits)
	atomic.StoreInt64(&cacheRequests, snapshot.Stats.TotalRequests)

	logutil.Logger.Infof("Cache loaded from disk: %d entries restored from %s", loadedCount, cfg.CachePersistenceFile)
	return nil
}

// StartCachePersistence starts the periodic cache persistence
func StartCachePersistence() {
	if !cfg.EnableCachePersistence {
		return
	}

	// Test cache persistence once during startup
	if err := ensureCacheDir(); err != nil {
		logutil.Logger.Errorf("Cache persistence disabled due to directory issues: %v", err)
		return
	}

	persistenceTicker = time.NewTicker(cfg.CachePersistenceInterval)
	go func() {
		defer persistenceTicker.Stop()
		for range persistenceTicker.C {
			if err := SaveCacheToFile(); err != nil {
				logutil.Logger.Errorf("Failed to persist cache: %v", err)
			}
		}
	}()

	logutil.Logger.Infof("Cache persistence enabled with interval: %v", cfg.CachePersistenceInterval)
}

// StopCachePersistence stops the periodic cache persistence and saves one final time
func StopCachePersistence() {
	if persistenceTicker != nil {
		persistenceTicker.Stop()
	}

	if cfg.EnableCachePersistence {
		if err := SaveCacheToFile(); err != nil {
			logutil.Logger.Errorf("Failed to save cache during shutdown: %v", err)
		} else {
			logutil.Logger.Info("Cache saved successfully during shutdown")
		}
	}
}

func StartCacheStatsLogger() {
	ticker := time.NewTicker(cfg.DNSStatslog)
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
						metric.GetFastMetricsInstance().FastUpdateCacheSize(itemCount)
					}
				}
			}
		}
	}()
}
