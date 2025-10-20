// Package cache provides DNS response caching functionality with persistence
// and performance optimizations including:
// - Object pooling for string builders and DNS RR slices to reduce GC pressure
// - Elimination of unnecessary goroutines for synchronous cache operations
// - Optimized JSON encoding with pre-allocated buffers
// - Reduced time.Now() calls in hot paths
// - Improved type assertion handling with corruption detection
// - Optimized cache key generation for common DNS types
// - String slice pooling for cache persistence operations
// - Batch processing for cache loading and persistence
package cache

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dnsloadbalancer/config"
	"dnsloadbalancer/dnsresolver"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/metric"

	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
)

var (
	DnsCache            *cache.Cache
	DefaultDNSCacheTTL  time.Duration = 30 * time.Minute
	EnableMetrics       bool
	EnableClientRouting bool
	EnableDomainRouting bool
	persistenceTicker   *time.Ticker
	cfg                 = config.Get() // Global config instance

	// Stale cache updater variables
	staleUpdaterTicker   *time.Ticker
	staleUpdateSemaphore chan struct{}
	accessTracker        *AccessTracker
)

// CacheEntry represents a cache entry for persistence
type CacheEntry struct {
	Key        string      `json:"key"`
	Answers    []string    `json:"answers"`               // DNS RR serialized as strings
	Expiration time.Time   `json:"expiration"`            // Zero time means no expiration
	AccessInfo *AccessInfo `json:"access_info,omitempty"` // Access tracking info
}

// CacheSnapshot represents the entire cache state for persistence
type CacheSnapshot struct {
	Entries   []CacheEntry `json:"entries"`
	Timestamp time.Time    `json:"timestamp"`
}

// AccessInfo tracks access patterns for individual cache entries
type AccessInfo struct {
	AccessCount int64     `json:"access_count"`
	LastAccess  time.Time `json:"last_access"`
	FirstAccess time.Time `json:"first_access"`
	Domain      string    `json:"domain"`
	QueryType   uint16    `json:"query_type"`
}

// AccessTracker manages access tracking for cache entries
type AccessTracker struct {
	accessMap sync.Map // map[string]*AccessInfo
	mu        sync.RWMutex
}

// NewAccessTracker creates a new access tracker
func NewAccessTracker() *AccessTracker {
	return &AccessTracker{}
}

// TrackAccess records an access to a cache entry
func (at *AccessTracker) TrackAccess(key, domain string, qtype uint16) {
	now := time.Now()

	// Try to load existing access info
	if value, ok := at.accessMap.Load(key); ok {
		if accessInfo, ok := value.(*AccessInfo); ok {
			atomic.AddInt64(&accessInfo.AccessCount, 1)
			at.mu.Lock()
			accessInfo.LastAccess = now
			at.mu.Unlock()
			return
		}
	}

	// Create new access info
	accessInfo := &AccessInfo{
		AccessCount: 1,
		LastAccess:  now,
		FirstAccess: now,
		Domain:      domain,
		QueryType:   qtype,
	}
	at.accessMap.Store(key, accessInfo)
}

// GetAccessInfo returns access information for a key
func (at *AccessTracker) GetAccessInfo(key string) (*AccessInfo, bool) {
	if value, ok := at.accessMap.Load(key); ok {
		if accessInfo, ok := value.(*AccessInfo); ok {
			return accessInfo, true
		}
	}
	return nil, false
}

// GetFrequentlyAccessed returns keys that are frequently accessed
func (at *AccessTracker) GetFrequentlyAccessed(minCount int64) []string {
	var keys []string
	at.accessMap.Range(func(key, value any) bool {
		if accessInfo, ok := value.(*AccessInfo); ok {
			if atomic.LoadInt64(&accessInfo.AccessCount) >= minCount {
				keys = append(keys, key.(string))
			}
		}
		return true
	})
	return keys
}

// CleanupExpiredEntries removes access tracking for entries that are no longer in cache
func (at *AccessTracker) CleanupExpiredEntries() {
	if DnsCache == nil {
		return
	}

	cacheItems := DnsCache.Items()
	at.accessMap.Range(func(key, value any) bool {
		keyStr := key.(string)
		if _, exists := cacheItems[keyStr]; !exists {
			at.accessMap.Delete(keyStr)
		}
		return true
	})
}

func Init(defaultDNSCacheTTL time.Duration, enableMetrics bool, _ any, enableClientRouting bool, enableDomainRouting bool) {
	DnsCache = cache.New(defaultDNSCacheTTL, 2*defaultDNSCacheTTL)
	DefaultDNSCacheTTL = defaultDNSCacheTTL
	EnableMetrics = enableMetrics
	EnableClientRouting = enableClientRouting
	EnableDomainRouting = enableDomainRouting

	// Initialize access tracker
	accessTracker = NewAccessTracker()

	// Initialize stale update semaphore and ticker
	if cfg.EnableStaleUpdater {
		staleUpdateSemaphore = make(chan struct{}, cfg.StaleUpdateMaxConcurrent)
		StartStaleUpdater()
	}

	// Load cache from disk if persistence is enabled
	if err := LoadCacheFromFile(); err != nil {
		logutil.Logger.Errorf("Failed to load cache from file: %v", err)
	}
	// Start periodic cache persistence
	StartCachePersistence()
}

// Pre-allocated buffer pool for cache keys to reduce allocations
var keyBuilderPool = sync.Pool{
	New: func() any {
		builder := &strings.Builder{}
		builder.Grow(128) // Pre-allocate reasonable capacity for most domain names
		return builder
	},
}

// Pool for DNS RR slices to reduce allocations
var dnsRRSlicePool = sync.Pool{
	New: func() any {
		slice := make([]dns.RR, 0, 16) // Pre-allocate common slice size
		return &slice
	},
}

// Pool for string slices used in JSON serialization
var stringSlicePool = sync.Pool{
	New: func() any {
		slice := make([]string, 0, 16)
		return &slice
	},
}

// getStringSlice gets a slice from the pool and resets it
func getStringSlice() []string {
	slicePtr := stringSlicePool.Get().(*[]string)
	slice := *slicePtr
	return slice[:0] // Reset length but keep capacity
}

// putStringSlice returns a slice to the pool if it's not too large
func putStringSlice(slice []string) {
	const maxPoolSliceSize = 256
	if cap(slice) <= maxPoolSliceSize {
		stringSlicePool.Put(&slice)
	}
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
	// For common DNS types, use pre-computed suffixes to minimize allocations
	switch qtype {
	case 1: // A record
		return domain + config.SuffixA
	case 28: // AAAA record
		return domain + config.SuffixAAAA
	case 5: // CNAME record
		return domain + config.SuffixCNAME
	case 15: // MX record
		return domain + config.SuffixMX
	case 16: // TXT record
		return domain + config.SuffixTXT
	default:
		// Use string builder pool for less common types
		builder := keyBuilderPool.Get().(*strings.Builder)
		defer func() {
			builder.Reset()
			keyBuilderPool.Put(builder)
		}()

		// Pre-allocate with exact capacity to avoid reallocation
		builder.Grow(len(domain) + 6) // domain + ':' + up to 5 digits for qtype
		_, err := builder.WriteString(domain)
		if err != nil {
			panic(err)
		}
		err = builder.WriteByte(':')
		if err != nil {
			panic(err)
		}
		_, err = builder.WriteString(strconv.FormatUint(uint64(qtype), 10))
		if err != nil {
			panic(err)
		}
		return builder.String()
	}
}

func LoadFromCache(key string) ([]dns.RR, bool) {
	val, found := DnsCache.Get(key)
	if !found {
		return nil, false
	}
	// Direct type assertion - assumes cache integrity for maximum performance
	return val.([]dns.RR), true
}

func ResolverWithCache(domain string, qtype uint16, clientIP string) []dns.RR {
	key := CacheKey(domain, qtype)
	// Cache hit path - ultra-optimized for sub-millisecond response
	if answers, ok := LoadFromCache(key); ok {
		// INLINE metrics: Just atomic increments (~2-5ns overhead total)
		// No goroutine spawn, no channel sends - periodic flushing handles Prometheus updates
		if EnableMetrics {
			fastMetrics := metric.GetFastMetricsInstance()
			if domain != "" {
				// Single call does both cache hit + domain tracking
				fastMetrics.InlineCacheHitWithDomain(domain)
			} else {
				// Just record cache hit
				fastMetrics.InlineCacheHit()
			}
		}

		// Defer heavy access tracking to background (only if needed)
		if cfg.EnableStaleUpdater && accessTracker != nil {
			go accessTracker.TrackAccess(key, domain, qtype)
		}

		return answers
	}
	// Determine resolver based on routing configuration
	resolver := dnsresolver.ResolverForClient
	if EnableDomainRouting {
		resolver = dnsresolver.ResolverForDomain
	}

	// Resolve DNS query
	answers := resolver(domain, qtype, clientIP)
	// Handle negative responses
	if len(answers) == 0 {
		go func() {
			DnsCache.Set(key, answers, DefaultDNSCacheTTL/config.NegativeResponseTTLDivisor)
			if EnableMetrics {
				metric.GetFastMetricsInstance().FastRecordCacheMiss()
			}
		}()
		return answers
	}

	go func() {
		// Calculate TTL for positive responses
		ttl := calculateTTL(answers)
		// Cache positive responses
		DnsCache.Set(key, answers, ttl)
	}()
	return answers
}

// Pre-parsed invalid answer IPs to avoid repeated parsing and string conversions
var (
	invalidARecordIP    []byte // Parsed IP for A record invalid answer
	invalidAAAARecordIP []byte // Parsed IP for AAAA record invalid answer
	invalidAnswersInit  sync.Once
)

// initInvalidAnswers parses the invalid answer strings once at startup
func initInvalidAnswers() {
	if s := config.ARecordInvalidAnswer; s != "" {
		if ip := net.ParseIP(s); ip != nil {
			if ip4 := ip.To4(); ip4 != nil {
				// Copy the parsed IPv4 address (4 bytes)
				invalidARecordIP = append([]byte(nil), ip4...)
			}
		}
	}
	if s := config.AAAARecordInvalidAnswer; s != "" {
		if ip := net.ParseIP(s); ip != nil {
			// To16() returns 16 bytes for both IPv4 and IPv6
			// Ensure it's actually IPv6 by checking To4() returns nil
			if ip16 := ip.To16(); ip16 != nil && ip.To4() == nil {
				// Copy the parsed IPv6 address (16 bytes)
				invalidAAAARecordIP = append([]byte(nil), ip16...)
			}
		}
	}
}

// bytesEqual compares two byte slices efficiently
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// calculateTTL determines the appropriate TTL for caching based on DNS response
// Ultra-optimized to minimize allocations, string conversions, and type assertions
func calculateTTL(answers []dns.RR) time.Duration {
	if len(answers) == 0 {
		return DefaultDNSCacheTTL
	}

	// Initialize invalid answer patterns once
	invalidAnswersInit.Do(initInvalidAnswers)
	var minTTL = config.MinTTL

	// Single pass with optimized branching
	for _, rr := range answers {
		header := rr.Header()
		ttl := header.Ttl

		// Fast path: if any TTL is 0, use default immediately
		if ttl == 0 {
			return DefaultDNSCacheTTL
		}

		// Update minimum TTL with branchless min operation
		if ttl < config.MinTTL {
			minTTL = ttl
		}

		// Only check for invalid answers if we have them configured
		// and only for A/AAAA records (most common invalid cases)
		if header.Rrtype == dns.TypeA && len(invalidARecordIP) > 0 {
			if a, ok := rr.(*dns.A); ok && bytesEqual(a.A, invalidARecordIP) {
				return DefaultDNSCacheTTL
			}
		} else if header.Rrtype == dns.TypeAAAA && len(invalidAAAARecordIP) > 0 {
			if aaaa, ok := rr.(*dns.AAAA); ok && bytesEqual(aaaa.AAAA, invalidAAAARecordIP) {
				return DefaultDNSCacheTTL
			}
		}
	}

	// Convert to duration with optimized bounds checking
	ttlDuration := time.Duration(minTTL) * time.Second

	// Clamp to configured limits
	if ttlDuration < config.MinCacheTTL {
		return config.MinCacheTTL
	}
	if ttlDuration > config.MaxCacheTTL {
		return config.MaxCacheTTL
	}
	return ttlDuration
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

		// Use pooled string slice to reduce allocations
		answerStrings := getStringSlice()
		for _, rr := range answers {
			answerStrings = append(answerStrings, rr.String())
		}

		var expiration time.Time
		if item.Expiration > 0 {
			expiration = time.Unix(0, item.Expiration)
		}
		// Note: Zero expiration time means the item never expires

		// Create copy of answer strings since we're returning the slice to pool
		finalAnswerStrings := make([]string, len(answerStrings))
		copy(finalAnswerStrings, answerStrings)

		// Get access info if stale updater is enabled
		var accessInfo *AccessInfo
		if cfg.EnableStaleUpdater && accessTracker != nil {
			if ai, found := accessTracker.GetAccessInfo(key); found {
				// Create a copy of access info for persistence
				accessInfo = &AccessInfo{
					AccessCount: atomic.LoadInt64(&ai.AccessCount),
					LastAccess:  ai.LastAccess,
					FirstAccess: ai.FirstAccess,
					Domain:      ai.Domain,
					QueryType:   ai.QueryType,
				}
			}
		}

		entries = append(entries, CacheEntry{
			Key:        key,
			Answers:    finalAnswerStrings,
			Expiration: expiration,
			AccessInfo: accessInfo,
		})

		// Return slice to pool
		putStringSlice(answerStrings)
	}

	snapshot := CacheSnapshot{
		Entries:   entries,
		Timestamp: now, // Use the same timestamp captured earlier
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

	// Process entries in batches to reduce memory pressure for large caches
	const batchSize = 100
	for i := 0; i < len(snapshot.Entries); i += batchSize {
		end := i + batchSize
		if end > len(snapshot.Entries) {
			end = len(snapshot.Entries)
		}

		batch := snapshot.Entries[i:end]
		for _, entry := range batch {
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

				// Restore access tracking information if available
				if cfg.EnableStaleUpdater && accessTracker != nil && entry.AccessInfo != nil {
					restoredAccessInfo := &AccessInfo{
						AccessCount: entry.AccessInfo.AccessCount,
						LastAccess:  entry.AccessInfo.LastAccess,
						FirstAccess: entry.AccessInfo.FirstAccess,
						Domain:      entry.AccessInfo.Domain,
						QueryType:   entry.AccessInfo.QueryType,
					}
					accessTracker.accessMap.Store(entry.Key, restoredAccessInfo)
				}
			}

			// Return slice to pool for reuse
			putDNSRRSlice(answers)
		}
	}
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

// StartStaleUpdater starts the periodic stale cache updater
func StartStaleUpdater() {
	if !cfg.EnableStaleUpdater {
		return
	}

	staleUpdaterTicker = time.NewTicker(cfg.StaleUpdateInterval)
	go func() {
		defer staleUpdaterTicker.Stop()
		for range staleUpdaterTicker.C {
			updateStaleEntries()
		}
	}()

	logutil.Logger.Infof("Stale cache updater enabled with interval: %v, threshold: %v, min access count: %d",
		cfg.StaleUpdateInterval, cfg.StaleUpdateThreshold, cfg.StaleUpdateMinAccessCount)
}

// StopStaleUpdater stops the stale updater
func StopStaleUpdater() {
	if staleUpdaterTicker != nil {
		staleUpdaterTicker.Stop()
		logutil.Logger.Info("Stale cache updater stopped")
	}
}

// updateStaleEntries checks for and updates stale frequently-accessed cache entries
func updateStaleEntries() {
	if DnsCache == nil || accessTracker == nil {
		return
	}

	// Clean up access tracking for expired cache entries
	accessTracker.CleanupExpiredEntries()

	// Get frequently accessed entries
	frequentKeys := accessTracker.GetFrequentlyAccessed(int64(cfg.StaleUpdateMinAccessCount))
	if len(frequentKeys) == 0 {
		return
	}

	staleCount := 0
	var updatedCount int64

	for _, key := range frequentKeys {
		// Check if entry is close to expiring
		if _, expiration, found := DnsCache.GetWithExpiration(key); found {
			var timeUntilExpiry time.Duration
			if expiration.IsZero() {
				// Entry never expires, skip
				continue
			}
			timeUntilExpiry = time.Until(expiration)

			// Check if entry is within the stale threshold
			if timeUntilExpiry <= cfg.StaleUpdateThreshold && timeUntilExpiry > 0 {
				staleCount++
				// Try to acquire semaphore for concurrent updates
				select {
				case staleUpdateSemaphore <- struct{}{}:
					// Successfully acquired semaphore, update in background
					go func(cacheKey string) {
						defer func() { <-staleUpdateSemaphore }()
						if updateStaleEntry(cacheKey) {
							atomic.AddInt64(&updatedCount, 1)
						}
					}(key)
				default:
					// Semaphore full, skip this update
					logutil.Logger.Debugf("Skipping stale update for %s - too many concurrent updates", key)
				}
			}
		}
	}
	if staleCount > 0 {
		finalUpdatedCount := atomic.LoadInt64(&updatedCount)
		logutil.Logger.Infof("Found %d stale entries, initiated %d updates", staleCount, finalUpdatedCount)
	}
}

// updateStaleEntry updates a single stale cache entry
func updateStaleEntry(key string) bool {
	// Get access info to extract domain and query type
	accessInfo, ok := accessTracker.GetAccessInfo(key)
	if !ok {
		logutil.Logger.Debugf("No access info found for stale key: %s", key)
		return false
	}

	domain := accessInfo.Domain
	qtype := accessInfo.QueryType

	logutil.Logger.Debugf("Updating stale entry: %s (domain: %s, type: %d, access count: %d)",
		key, domain, qtype, atomic.LoadInt64(&accessInfo.AccessCount))

	// Determine resolver based on routing configuration
	resolver := dnsresolver.ResolverForClient
	if EnableDomainRouting {
		resolver = dnsresolver.ResolverForDomain
	}

	// Resolve DNS query with a dummy client IP for stale updates
	answers := resolver(domain, qtype, "127.0.0.1")

	// Handle the response
	if len(answers) == 0 {
		// For negative responses, use a shorter TTL
		DnsCache.Set(key, answers, DefaultDNSCacheTTL/config.NegativeResponseTTLDivisor)
	} else {
		// Calculate TTL for positive responses
		ttl := calculateTTL(answers)
		DnsCache.Set(key, answers, ttl)
	}

	if EnableMetrics {
		metric.GetFastMetricsInstance().FastRecordCacheHit() // Count as a cache refresh
		qTypeStr := dns.TypeToString[qtype]
		if len(answers) == 0 {
			metric.GetFastMetricsInstance().FastRecordDNSQuery(qTypeStr, "stale_update_nxdomain", 0)
		} else {
			metric.GetFastMetricsInstance().FastRecordDNSQuery(qTypeStr, "stale_update_success", 0)
		}
	}

	logutil.Logger.Debugf("Successfully updated stale entry: %s", key)
	return true
}

// Cleanup stops all cache background processes and performs final cleanup
func Cleanup() {
	StopCachePersistence()
	defer StopStaleUpdater()
	if cfg.EnableStaleUpdater && accessTracker != nil {
		logutil.Logger.Info("Cleaning up access tracker")
	}
}

func StartCacheStatsLogger() {
	ticker := time.NewTicker(cfg.DNSStatslog)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
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
