package cache

import (
	"testing"
	"time"

	"dnsloadbalancer/config"

	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/assert"
)

func setupTestStats() func() {
	oldCache := DnsCache
	oldCfg := cfg
	oldEnableMetrics := EnableMetrics

	DnsCache = cache.New(5*time.Minute, 10*time.Minute)
	cfg = config.Get()
	cfg.DNSStatslog = 50 * time.Millisecond
	EnableMetrics = false

	return func() {
		DnsCache = oldCache
		cfg = oldCfg
		EnableMetrics = oldEnableMetrics
	}
}

func TestStartCacheStatsLogger(t *testing.T) {
	cleanup := setupTestStats()
	defer cleanup()

	// Add some test data
	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	DnsCache.Set("example.com:1", []dns.RR{rr}, 5*time.Minute)

	// Start stats logger
	StartCacheStatsLogger()

	// Wait for at least one log cycle
	time.Sleep(100 * time.Millisecond)

	// Verify cache still has the entry
	_, found := DnsCache.Get("example.com:1")
	assert.True(t, found)
}

func TestStartCacheStatsLogger_EmptyCache(t *testing.T) {
	cleanup := setupTestStats()
	defer cleanup()

	// Start stats logger with empty cache
	StartCacheStatsLogger()

	// Wait for at least one log cycle
	time.Sleep(100 * time.Millisecond)

	// Should not panic
	items := DnsCache.Items()
	assert.Empty(t, items)
}

func TestStartCacheStatsLogger_NilCache(t *testing.T) {
	cleanup := setupTestStats()
	defer cleanup()

	oldCache := DnsCache
	DnsCache = nil
	defer func() { DnsCache = oldCache }()

	// Should not panic with nil cache
	assert.NotPanics(t, func() {
		StartCacheStatsLogger()
		time.Sleep(100 * time.Millisecond)
	})
}

func TestStartCacheStatsLogger_WithMetrics(t *testing.T) {
	cleanup := setupTestStats()
	defer cleanup()

	EnableMetrics = true

	// Add test data
	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	DnsCache.Set("example.com:1", []dns.RR{rr}, 5*time.Minute)

	// Start stats logger
	StartCacheStatsLogger()

	// Wait for at least one log cycle
	time.Sleep(100 * time.Millisecond)

	// Verify cache entry
	_, found := DnsCache.Get("example.com:1")
	assert.True(t, found)
}

func TestStartCacheStatsLogger_MultipleEntries(t *testing.T) {
	cleanup := setupTestStats()
	defer cleanup()

	// Add multiple test entries
	for i := 0; i < 10; i++ {
		key := "example" + string(rune('0'+i)) + ".com:1"
		rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
		DnsCache.Set(key, []dns.RR{rr}, 5*time.Minute)
	}

	// Start stats logger
	StartCacheStatsLogger()

	// Wait for at least one log cycle
	time.Sleep(100 * time.Millisecond)

	// Verify all entries are present
	items := DnsCache.Items()
	assert.Len(t, items, 10)
}

func BenchmarkStartCacheStatsLogger(b *testing.B) {
	DnsCache = cache.New(5*time.Minute, 10*time.Minute)
	cfg = config.Get()
	cfg.DNSStatslog = 1 * time.Hour // Set to a long interval for benchmark
	EnableMetrics = false

	// Populate cache
	for i := 0; i < 1000; i++ {
		key := "example" + string(rune(i)) + ".com:1"
		rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
		DnsCache.Set(key, []dns.RR{rr}, 5*time.Minute)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		items := DnsCache.Items()
		_ = len(items)
	}
}
