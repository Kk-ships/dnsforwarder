package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"dnsloadbalancer/logutil"

	"github.com/miekg/dns"
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

	// Check if cache is initialized
	if DnsCache == nil {
		return fmt.Errorf("cache not initialized")
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
					AccessCount:      atomic.LoadInt64(&ai.AccessCount),
					LastAccessNanos:  atomic.LoadInt64(&ai.LastAccessNanos),
					FirstAccessNanos: atomic.LoadInt64(&ai.FirstAccessNanos),
					Domain:           ai.Domain,
					QueryType:        ai.QueryType,
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
						AccessCount:      entry.AccessInfo.AccessCount,
						LastAccessNanos:  entry.AccessInfo.LastAccessNanos,
						FirstAccessNanos: entry.AccessInfo.FirstAccessNanos,
						Domain:           entry.AccessInfo.Domain,
						QueryType:        entry.AccessInfo.QueryType,
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

	// Check if cache is initialized before starting persistence
	if DnsCache == nil {
		logutil.Logger.Warn("Cache not initialized, skipping cache persistence")
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
