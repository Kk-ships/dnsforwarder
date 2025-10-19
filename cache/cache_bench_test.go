package cache

import (
	"testing"
	"time"

	"dnsloadbalancer/config"

	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
)

// BenchmarkCacheHit measures the performance of cached DNS responses
func BenchmarkCacheHit(b *testing.B) {
	// Initialize cache with test data
	DnsCache = cache.New(5*time.Minute, 10*time.Minute)
	EnableMetrics = false // Disable metrics for pure cache performance
	EnableClientRouting = false
	EnableDomainRouting = false

	// Pre-populate cache with test data
	testAnswers := []dns.RR{
		&dns.A{
			Hdr: dns.RR_Header{
				Name:   "example.com.",
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: []byte{192, 168, 1, 1},
		},
	}

	testKey := CacheKey("example.com", 1)
	DnsCache.Set(testKey, testAnswers, 5*time.Minute)

	b.ResetTimer()

	// Measure cache hit performance
	for i := 0; i < b.N; i++ {
		result := ResolverWithCache("example.com", 1, "192.168.1.100")
		if len(result) == 0 {
			b.Fatal("Expected cached result")
		}
	}
}

// BenchmarkCacheKeyGeneration measures cache key generation performance
func BenchmarkCacheKeyGeneration(b *testing.B) {
	domain := "example.com"

	b.Run("A_Record", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = CacheKey(domain, 1) // A record
		}
	})

	b.Run("AAAA_Record", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = CacheKey(domain, 28) // AAAA record
		}
	})

	b.Run("Uncommon_Record", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = CacheKey(domain, 99) // Uncommon record type
		}
	})
}

// BenchmarkFastLoadFromCache measures the optimized cache load performance
func BenchmarkFastLoadFromCache(b *testing.B) {
	DnsCache = cache.New(5*time.Minute, 10*time.Minute)

	testAnswers := []dns.RR{
		&dns.A{
			Hdr: dns.RR_Header{
				Name:   "example.com.",
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: []byte{192, 168, 1, 1},
		},
	}

	testKey := "example.com:1"
	DnsCache.Set(testKey, testAnswers, 5*time.Minute)

	b.ResetTimer()

	b.Run("FastLoad", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, ok := LoadFromCache(testKey)
			if !ok {
				b.Fatal("Expected cache hit")
			}
		}
	})

	b.Run("RegularLoad", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, ok := LoadFromCache(testKey)
			if !ok {
				b.Fatal("Expected cache hit")
			}
		}
	})
}

// BenchmarkCacheHitWithMetrics compares performance with and without metrics
func BenchmarkCacheHitWithMetrics(b *testing.B) {
	DnsCache = cache.New(5*time.Minute, 10*time.Minute)
	EnableClientRouting = false
	EnableDomainRouting = false

	testAnswers := []dns.RR{
		&dns.A{
			Hdr: dns.RR_Header{
				Name:   "benchmark.com.",
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: []byte{10, 0, 0, 1},
		},
	}

	testKey := CacheKey("benchmark.com", 1)
	DnsCache.Set(testKey, testAnswers, 5*time.Minute)

	b.Run("WithoutMetrics", func(b *testing.B) {
		EnableMetrics = false
		cfg = &config.Config{EnableStaleUpdater: false}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			result := ResolverWithCache("benchmark.com", 1, "10.0.0.1")
			if len(result) == 0 {
				b.Fatal("Expected cached result")
			}
		}
	})

	b.Run("WithMetrics", func(b *testing.B) {
		EnableMetrics = true
		cfg = &config.Config{EnableStaleUpdater: false}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			result := ResolverWithCache("benchmark.com", 1, "10.0.0.1")
			if len(result) == 0 {
				b.Fatal("Expected cached result")
			}
		}
	})
}

// BenchmarkCalculateTTL measures the performance of TTL calculation
func BenchmarkCalculateTTL(b *testing.B) {
	// Create test DNS RR with different scenarios
	testAnswersSingle := []dns.RR{
		&dns.A{
			Hdr: dns.RR_Header{
				Name:   "example.com.",
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: []byte{192, 168, 1, 1},
		},
	}

	testAnswersMultiple := []dns.RR{
		&dns.A{
			Hdr: dns.RR_Header{
				Name:   "example.com.",
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: []byte{192, 168, 1, 1},
		},
		&dns.A{
			Hdr: dns.RR_Header{
				Name:   "example.com.",
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    600,
			},
			A: []byte{192, 168, 1, 2},
		},
		&dns.AAAA{
			Hdr: dns.RR_Header{
				Name:   "example.com.",
				Rrtype: dns.TypeAAAA,
				Class:  dns.ClassINET,
				Ttl:    1200,
			},
			AAAA: []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
		},
	}

	b.Run("SingleAnswer", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = calculateTTL(testAnswersSingle)
		}
	})

	b.Run("MultipleAnswers", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = calculateTTL(testAnswersMultiple)
		}
	})

	b.Run("EmptyAnswers", func(b *testing.B) {
		emptyAnswers := []dns.RR{}
		for i := 0; i < b.N; i++ {
			_ = calculateTTL(emptyAnswers)
		}
	})
}
