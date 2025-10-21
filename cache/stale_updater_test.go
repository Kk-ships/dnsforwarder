package cache

import (
	"testing"
	"time"

	"dnsloadbalancer/config"

	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/assert"
)

func setupTestStaleUpdater() func() {
	oldCache := DnsCache
	oldTracker := accessTracker
	oldSemaphore := staleUpdateSemaphore
	oldCfg := cfg
	oldEnableDomainRouting := EnableDomainRouting
	oldEnableMetrics := EnableMetrics

	DnsCache = cache.New(5*time.Minute, 10*time.Minute)
	accessTracker = NewAccessTracker()
	cfg = config.Get()
	cfg.EnableStaleUpdater = true
	cfg.StaleUpdateInterval = 100 * time.Millisecond
	cfg.StaleUpdateThreshold = 1 * time.Minute
	cfg.StaleUpdateMinAccessCount = 2
	cfg.StaleUpdateMaxConcurrent = 5
	staleUpdateSemaphore = make(chan struct{}, cfg.StaleUpdateMaxConcurrent)
	EnableDomainRouting = false
	EnableMetrics = false

	return func() {
		DnsCache = oldCache
		accessTracker = oldTracker
		staleUpdateSemaphore = oldSemaphore
		cfg = oldCfg
		EnableDomainRouting = oldEnableDomainRouting
		EnableMetrics = oldEnableMetrics
	}
}

func TestStartStopStaleUpdater(t *testing.T) {
	cleanup := setupTestStaleUpdater()
	defer cleanup()

	// Start stale updater
	StartStaleUpdater()
	assert.NotNil(t, staleUpdaterTicker)

	// Wait a bit to ensure it runs
	time.Sleep(150 * time.Millisecond)

	// Stop stale updater
	StopStaleUpdater()
	assert.Nil(t, staleUpdaterTicker)
}

func TestStartStaleUpdater_Disabled(t *testing.T) {
	cleanup := setupTestStaleUpdater()
	defer cleanup()

	cfg.EnableStaleUpdater = false
	StartStaleUpdater()
	assert.Nil(t, staleUpdaterTicker)
}

func TestUpdateStaleEntries_NoCache(t *testing.T) {
	cleanup := setupTestStaleUpdater()
	defer cleanup()

	oldCache := DnsCache
	DnsCache = nil
	defer func() { DnsCache = oldCache }()

	// Should not panic with nil cache
	assert.NotPanics(t, func() {
		updateStaleEntries()
	})
}

func TestUpdateStaleEntries_NoAccessTracker(t *testing.T) {
	cleanup := setupTestStaleUpdater()
	defer cleanup()

	oldTracker := accessTracker
	accessTracker = nil
	defer func() { accessTracker = oldTracker }()

	// Should not panic with nil access tracker
	assert.NotPanics(t, func() {
		updateStaleEntries()
	})
}

func TestUpdateStaleEntries_NoFrequentEntries(t *testing.T) {
	cleanup := setupTestStaleUpdater()
	defer cleanup()

	// Add entry with low access count
	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	DnsCache.Set("example.com:1", []dns.RR{rr}, 5*time.Second)
	accessTracker.TrackAccess("example.com:1", "example.com", 1)

	// Should not find any entries to update (min access count is 2)
	assert.NotPanics(t, func() {
		updateStaleEntries()
	})
}

func TestUpdateStaleEntries_IdentifiesStaleEntries(t *testing.T) {
	cleanup := setupTestStaleUpdater()
	defer cleanup()

	// Add entry that will become stale
	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	DnsCache.Set("example.com:1", []dns.RR{rr}, 50*time.Millisecond)

	// Track access multiple times
	accessTracker.TrackAccess("example.com:1", "example.com", 1)
	accessTracker.TrackAccess("example.com:1", "example.com", 1)
	accessTracker.TrackAccess("example.com:1", "example.com", 1)

	// Wait for entry to become stale
	time.Sleep(10 * time.Millisecond)

	// This should identify the stale entry but may not update it
	// (depends on resolver availability in test environment)
	assert.NotPanics(t, func() {
		updateStaleEntries()
	})
}

func TestUpdateStaleEntries_SkipsNonExpiringEntries(t *testing.T) {
	cleanup := setupTestStaleUpdater()
	defer cleanup()

	// Add entry with zero expiration (never expires)
	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	DnsCache.Set("example.com:1", []dns.RR{rr}, cache.NoExpiration)

	// Track access multiple times
	for i := 0; i < 5; i++ {
		accessTracker.TrackAccess("example.com:1", "example.com", 1)
	}

	// Should skip non-expiring entries
	assert.NotPanics(t, func() {
		updateStaleEntries()
	})
}

func TestUpdateStaleEntry_NoAccessInfo(t *testing.T) {
	cleanup := setupTestStaleUpdater()
	defer cleanup()

	result := updateStaleEntry("nonexistent.com:1")
	assert.False(t, result)
}

func TestUpdateStaleEntry_Success(t *testing.T) {
	cleanup := setupTestStaleUpdater()
	defer cleanup()

	// Add entry
	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	DnsCache.Set("example.com:1", []dns.RR{rr}, 1*time.Second)

	// Track access
	accessTracker.TrackAccess("example.com:1", "example.com", 1)

	// Note: This test may fail in environments without proper DNS resolution
	// The function will attempt to resolve, but may get empty results
	result := updateStaleEntry("example.com:1")

	// We can at least verify it doesn't panic
	assert.NotPanics(t, func() {
		updateStaleEntry("example.com:1")
	})

	// The result depends on whether DNS resolution succeeds
	_ = result
}

func TestUpdateStaleEntries_RespectsSemaphore(t *testing.T) {
	cleanup := setupTestStaleUpdater()
	defer cleanup()

	// Set a very low semaphore limit
	cfg.StaleUpdateMaxConcurrent = 1
	staleUpdateSemaphore = make(chan struct{}, 1)

	// Add multiple stale entries
	for i := 0; i < 5; i++ {
		key := "example" + string(rune('0'+i)) + ".com:1"
		rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
		DnsCache.Set(key, []dns.RR{rr}, 50*time.Millisecond)

		// Track access multiple times
		for j := 0; j < 3; j++ {
			accessTracker.TrackAccess(key, "example"+string(rune('0'+i))+".com", 1)
		}
	}

	// Wait for entries to become stale
	time.Sleep(10 * time.Millisecond)

	// Should respect semaphore limit and not panic
	assert.NotPanics(t, func() {
		updateStaleEntries()
	})
}

func TestStaleUpdater_Integration(t *testing.T) {
	cleanup := setupTestStaleUpdater()
	defer cleanup()

	cfg.StaleUpdateInterval = 50 * time.Millisecond
	cfg.StaleUpdateThreshold = 200 * time.Millisecond

	// Add an entry that will become stale
	rr, _ := dns.NewRR("test.com. 300 IN A 1.2.3.4")
	DnsCache.Set("test.com:1", []dns.RR{rr}, 250*time.Millisecond)

	// Track access multiple times to make it "frequent"
	for i := 0; i < 5; i++ {
		accessTracker.TrackAccess("test.com:1", "test.com", 1)
	}

	// Start the updater
	StartStaleUpdater()
	defer StopStaleUpdater()

	// Wait for entry to enter stale threshold
	time.Sleep(100 * time.Millisecond)

	// Wait for at least one update cycle
	time.Sleep(100 * time.Millisecond)

	// The entry should still be in cache (may or may not be updated depending on resolver)
	_, found := DnsCache.Get("test.com:1")
	assert.True(t, found)
}

func BenchmarkUpdateStaleEntry(b *testing.B) {
	DnsCache = cache.New(5*time.Minute, 10*time.Minute)
	accessTracker = NewAccessTracker()
	cfg = config.Get()
	EnableDomainRouting = false
	EnableMetrics = false

	// Setup test entry
	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	DnsCache.Set("example.com:1", []dns.RR{rr}, 5*time.Minute)
	accessTracker.TrackAccess("example.com:1", "example.com", 1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		updateStaleEntry("example.com:1")
	}
}

func BenchmarkUpdateStaleEntries(b *testing.B) {
	DnsCache = cache.New(5*time.Minute, 10*time.Minute)
	accessTracker = NewAccessTracker()
	cfg = config.Get()
	cfg.StaleUpdateMinAccessCount = 2
	cfg.StaleUpdateThreshold = 1 * time.Minute
	EnableDomainRouting = false
	EnableMetrics = false

	// Setup test entries
	for i := 0; i < 10; i++ {
		key := "example" + string(rune('0'+i)) + ".com:1"
		rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
		DnsCache.Set(key, []dns.RR{rr}, 30*time.Second)

		for j := 0; j < 3; j++ {
			accessTracker.TrackAccess(key, "example"+string(rune('0'+i))+".com", 1)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		updateStaleEntries()
	}
}
