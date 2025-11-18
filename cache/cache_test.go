package cache

import (
	"sync"
	"testing"
	"time"

	"dnsloadbalancer/config"

	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/assert"
)

func setupTestCacheCore() func() {
	oldCache := DnsCache
	oldCfg := cfg
	oldEnableMetrics := EnableMetrics
	oldEnableDomainRouting := EnableDomainRouting

	cfg = config.Get()
	cfg.EnableStaleUpdater = false
	cfg.EnableCachePersistence = false
	EnableMetrics = false
	EnableDomainRouting = false

	return func() {
		DnsCache = oldCache
		cfg = oldCfg
		EnableMetrics = oldEnableMetrics
		EnableDomainRouting = oldEnableDomainRouting
	}
}

func TestInit(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	Init(5*time.Minute, false, nil, false, false)

	assert.NotNil(t, DnsCache)
	assert.Equal(t, 5*time.Minute, DefaultDNSCacheTTL)
	assert.False(t, EnableMetrics)
	assert.False(t, EnableClientRouting)
	assert.False(t, EnableDomainRouting)
	assert.NotNil(t, accessTracker)
}

func TestInit_WithStaleUpdater(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	cfg.EnableStaleUpdater = true
	cfg.StaleUpdateMaxConcurrent = 5

	Init(5*time.Minute, false, nil, false, false)

	assert.NotNil(t, DnsCache)
	assert.NotNil(t, staleUpdateSemaphore)
	assert.NotNil(t, accessTracker)

	// Cleanup
	StopStaleUpdater()
}

func TestCacheKey_CommonTypes(t *testing.T) {
	tests := []struct {
		name     string
		domain   string
		qtype    uint16
		expected string
	}{
		{"A record", "example.com", 1, "example.com." + config.SuffixA},
		{"AAAA record", "example.com", 28, "example.com." + config.SuffixAAAA},
		{"CNAME record", "example.com", 5, "example.com." + config.SuffixCNAME},
		{"MX record", "example.com", 15, "example.com." + config.SuffixMX},
		{"TXT record", "example.com", 16, "example.com." + config.SuffixTXT},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := CacheKey(tt.domain, tt.qtype)
			assert.Equal(t, tt.expected, key)
		})
	}
}

func TestCacheKey_UncommonType(t *testing.T) {
	// Test with an uncommon DNS type (e.g., NS record = type 2)
	key := CacheKey("example.com", 2)
	assert.Equal(t, "example.com.:2", key)
}

func TestCacheKey_Concurrent(t *testing.T) {
	// Test concurrent cache key generation
	done := make(chan bool)
	numGoroutines := 100

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			key := CacheKey("example.com", 1)
			assert.NotEmpty(t, key)
			done <- true
		}(i)
	}

	for i := 0; i < numGoroutines; i++ {
		<-done
	}
}

func TestLoadFromCache_Found(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	DnsCache = cache.New(5*time.Minute, 10*time.Minute)

	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	DnsCache.Set("example.com.:1", []dns.RR{rr}, 5*time.Minute)

	answers, found := LoadFromCache("example.com.:1")
	assert.True(t, found)
	assert.Len(t, answers, 1)
}

func TestLoadFromCache_NotFound(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	DnsCache = cache.New(5*time.Minute, 10*time.Minute)

	answers, found := LoadFromCache("nonexistent.com:1")
	assert.False(t, found)
	assert.Nil(t, answers)
}

func TestResolverWithCache_CacheHit(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	DnsCache = cache.New(5*time.Minute, 10*time.Minute)

	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	expectedAnswers := []dns.RR{rr}
	DnsCache.Set("example.com.:1", expectedAnswers, 5*time.Minute)

	answers := ResolverWithCache("example.com", 1, "127.0.0.1")
	assert.Equal(t, expectedAnswers, answers)
}

func TestCalculateTTL_EmptyAnswers(t *testing.T) {
	ttl := calculateTTL([]dns.RR{})
	assert.Equal(t, DefaultDNSCacheTTL, ttl)
}

func TestCalculateTTL_ZeroTTL(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	rr, _ := dns.NewRR("example.com. 0 IN A 1.2.3.4")
	ttl := calculateTTL([]dns.RR{rr})
	assert.Equal(t, DefaultDNSCacheTTL, ttl)
}

func TestCalculateTTL_NormalTTL(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	ttl := calculateTTL([]dns.RR{rr})

	expected := 300 * time.Second
	assert.Equal(t, expected, ttl)
}

func TestCalculateTTL_BelowMinimum(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	// TTL of 10 seconds is below the minimum (30 seconds)
	rr, _ := dns.NewRR("example.com. 10 IN A 1.2.3.4")
	ttl := calculateTTL([]dns.RR{rr})

	// Should be clamped to config.MinCacheTTL (30 seconds)
	assert.Equal(t, config.MinCacheTTL, ttl)
}

func TestCalculateTTL_AboveMaximum(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	// TTL of 7200 seconds (2 hours) is above the maximum (24 hours by default, but let's test with something clearly above any reasonable value)
	// Actually 7200 seconds = 2 hours which is below 24 hours, so let's use a larger value
	rr, _ := dns.NewRR("example.com. 86401 IN A 1.2.3.4")
	ttl := calculateTTL([]dns.RR{rr})

	// Should be clamped to config.MaxCacheTTL (24 hours)
	assert.Equal(t, config.MaxCacheTTL, ttl)
}

func TestCalculateTTL_InvalidARecord(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	// Set invalid answer IP
	invalidAnswersInit = sync.Once{} // Reset to reinitialize

	rr, _ := dns.NewRR("example.com. 300 IN A 0.0.0.0")
	ttl := calculateTTL([]dns.RR{rr})

	assert.Equal(t, DefaultDNSCacheTTL, ttl)
}

func TestCalculateTTL_InvalidAAAARecord(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	// Set invalid answer IP
	invalidAnswersInit = sync.Once{} // Reset to reinitialize

	rr, _ := dns.NewRR("example.com. 300 IN AAAA ::")
	ttl := calculateTTL([]dns.RR{rr})

	assert.Equal(t, DefaultDNSCacheTTL, ttl)
}

func TestCalculateTTL_MultipleRecords(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	rr1, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	rr2, _ := dns.NewRR("example.com. 600 IN A 5.6.7.8")

	ttl := calculateTTL([]dns.RR{rr1, rr2})

	// Should use minimum TTL from the records (300 seconds)
	expected := 300 * time.Second
	assert.Equal(t, expected, ttl)
}

func TestBytesEqual(t *testing.T) {
	tests := []struct {
		name     string
		a        []byte
		b        []byte
		expected bool
	}{
		{"Equal slices", []byte{1, 2, 3}, []byte{1, 2, 3}, true},
		{"Different slices", []byte{1, 2, 3}, []byte{1, 2, 4}, false},
		{"Different lengths", []byte{1, 2}, []byte{1, 2, 3}, false},
		{"Empty slices", []byte{}, []byte{}, true},
		{"One empty", []byte{1}, []byte{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bytesEqual(tt.a, tt.b)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCleanup(t *testing.T) {
	cleanup := setupTestCacheCore()
	defer cleanup()

	Init(5*time.Minute, false, nil, false, false)

	// Should not panic
	assert.NotPanics(t, func() {
		Cleanup()
	})
}

func TestInitInvalidAnswers(t *testing.T) {
	// Reset the sync.Once to allow reinitialization
	invalidAnswersInit = sync.Once{}

	initInvalidAnswers()

	assert.NotNil(t, invalidARecordIP)
	assert.NotNil(t, invalidAAAARecordIP)
	assert.Len(t, invalidARecordIP, 4)     // IPv4 is 4 bytes
	assert.Len(t, invalidAAAARecordIP, 16) // IPv6 is 16 bytes
}

func TestInitInvalidAnswers_EmptyConfig(t *testing.T) {
	// Reset the sync.Once to allow reinitialization
	invalidAnswersInit = sync.Once{}
	// Reset the variables to nil to simulate empty config
	invalidARecordIP = nil
	invalidAAAARecordIP = nil

	initInvalidAnswers()

	// With the current implementation, these will be set to the default values
	// from the config constants (0.0.0.0 and ::), not nil
	assert.NotNil(t, invalidARecordIP)
	assert.NotNil(t, invalidAAAARecordIP)
	assert.Len(t, invalidARecordIP, 4)     // IPv4 is 4 bytes
	assert.Len(t, invalidAAAARecordIP, 16) // IPv6 is 16 bytes
}

func BenchmarkCacheKey_CommonType(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CacheKey("example.com", 1)
	}
}

func BenchmarkCacheKey_UncommonType(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CacheKey("example.com", 99)
	}
}

func BenchmarkLoadFromCache_Hit(b *testing.B) {
	DnsCache = cache.New(5*time.Minute, 10*time.Minute)
	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	DnsCache.Set("example.com:1", []dns.RR{rr}, 5*time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		LoadFromCache("example.com:1")
	}
}

func BenchmarkLoadFromCache_Miss(b *testing.B) {
	DnsCache = cache.New(5*time.Minute, 10*time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		LoadFromCache("nonexistent.com:1")
	}
}

func BenchmarkCalculateTTL(b *testing.B) {
	rr1, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	rr2, _ := dns.NewRR("example.com. 600 IN A 5.6.7.8")
	answers := []dns.RR{rr1, rr2}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calculateTTL(answers)
	}
}

func BenchmarkBytesEqual(b *testing.B) {
	a := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	b2 := []byte{1, 2, 3, 4, 5, 6, 7, 8}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bytesEqual(a, b2)
	}
}

func BenchmarkCacheKey_Concurrent(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			CacheKey("example.com", 1)
		}
	})
}
