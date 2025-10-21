package cache

import (
	"sync/atomic"
	"time"

	"dnsloadbalancer/config"
	"dnsloadbalancer/dnsresolver"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/metric"

	"github.com/miekg/dns"
)

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
		staleUpdaterTicker = nil
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
		logutil.Logger.Debugf("Found %d stale entries, initiated %d updates", staleCount, finalUpdatedCount)
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
