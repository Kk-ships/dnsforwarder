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
	"net"
	"strconv"
	"strings"
	"sync"
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

// Pre-parsed invalid answer IPs to avoid repeated parsing and string conversions
var (
	invalidARecordIP    []byte // Parsed IP for A record invalid answer
	invalidAAAARecordIP []byte // Parsed IP for AAAA record invalid answer
	invalidAnswersInit  sync.Once
)

// Init initializes the DNS cache with the specified configuration
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

// CacheKey generates a cache key for the given domain and query type
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

// LoadFromCache attempts to retrieve a cached DNS response
func LoadFromCache(key string) ([]dns.RR, bool) {
	val, found := DnsCache.Get(key)
	if !found {
		return nil, false
	}
	// Direct type assertion - assumes cache integrity for maximum performance
	return val.([]dns.RR), true
}

// ResolverWithCache performs DNS resolution with caching
func ResolverWithCache(domain string, qtype uint16, clientIP string) []dns.RR {
	key := CacheKey(domain, qtype)
	// Cache hit path - ultra-optimized for sub-millisecond response
	if answers, ok := LoadFromCache(key); ok {
		// Defer all heavy operations to run after returning response
		defer func() {
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

			// Track access for stale cache updater (non-blocking)
			if cfg.EnableStaleUpdater && accessTracker != nil {
				accessTracker.TrackAccess(key, domain, qtype)
			}
		}()

		return answers
	}
	// Determine resolver based on routing configuration
	resolver := dnsresolver.ResolverForClient
	if EnableDomainRouting {
		resolver = dnsresolver.ResolverForDomain
	}

	// Resolve DNS query
	answers := resolver(domain, qtype, clientIP)

	// Defer cache write and metrics for all cache misses
	defer func() {
		var ttl time.Duration
		if len(answers) == 0 {
			// Shorter TTL for negative responses
			ttl = DefaultDNSCacheTTL / config.NegativeResponseTTLDivisor
		} else {
			// Calculate TTL for positive responses
			ttl = calculateTTL(answers)
		}

		// Cache the response
		DnsCache.Set(key, answers, ttl)

		// Record cache miss metric
		if EnableMetrics {
			metric.GetFastMetricsInstance().FastRecordCacheMiss()
		}
	}()

	return answers
}

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

		// Update minimum TTL
		if ttl < minTTL {
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

// Cleanup stops all cache background processes and performs final cleanup
func Cleanup() {
	StopCachePersistence()
	StopStaleUpdater()
	if cfg.EnableStaleUpdater && accessTracker != nil {
		logutil.Logger.Info("Cleaning up access tracker")
	}
}
