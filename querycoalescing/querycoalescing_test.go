package querycoalescing

import (
	"dnsloadbalancer/config"
	"dnsloadbalancer/metric"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestQueryCoalescing(t *testing.T) {
	// Create test config
	cfg := &config.Config{
		EnableQueryCoalescing:          true,
		QueryCoalescingTimeout:         5 * time.Second,
		QueryCoalescingCleanupInterval: 1 * time.Minute,
	}

	// Create test metrics recorder
	metrics := metric.NewFastMetricsRecorder()
	defer metrics.Close()

	// Create query coalescer
	coalescer := NewQueryCoalescer(cfg, metrics)
	defer coalescer.Cleanup()

	// Test concurrent identical queries
	const numWorkers = 10
	const testDomain = "example.com."
	const testType = dns.TypeA

	key := QueryKey(testDomain, testType, "", false)

	var callCount int64
	var wg sync.WaitGroup
	results := make([][]dns.RR, numWorkers)

	// Define a query function that simulates upstream DNS resolution
	queryFunc := func() ([]dns.RR, error) {
		atomic.AddInt64(&callCount, 1)
		// Simulate some processing time
		time.Sleep(100 * time.Millisecond)

		// Return mock A record
		rr, _ := dns.NewRR("example.com. 300 IN A 192.168.1.1")
		return []dns.RR{rr}, nil
	}

	// Launch multiple concurrent queries
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			answers, err := coalescer.CoalesceQuery(key, queryFunc)
			if err != nil {
				t.Errorf("Query %d failed: %v", index, err)
				return
			}
			results[index] = answers
		}(i)
	}

	wg.Wait()

	// Verify that the query function was called only once (coalescing worked)
	actualCallCount := atomic.LoadInt64(&callCount)
	if actualCallCount != 1 {
		t.Errorf("Expected query function to be called once, but was called %d times", actualCallCount)
	}

	// Verify that all workers got the same result
	expectedResult := "example.com.\t300\tIN\tA\t192.168.1.1"
	for i, result := range results {
		if len(result) != 1 {
			t.Errorf("Worker %d got %d results, expected 1", i, len(result))
			continue
		}
		if result[0].String() != expectedResult {
			t.Errorf("Worker %d got result '%s', expected '%s'", i, result[0].String(), expectedResult)
		}
	}

	// Test that stats are working
	stats := coalescer.GetStats()
	t.Logf("Coalescing stats: %+v", stats)
}

func TestQueryCoalescingDisabled(t *testing.T) {
	// Create config with coalescing disabled
	cfg := &config.Config{
		EnableQueryCoalescing: false,
	}

	// Create test metrics recorder
	metrics := metric.NewFastMetricsRecorder()
	defer metrics.Close()

	// Create query coalescer
	coalescer := NewQueryCoalescer(cfg, metrics)
	defer coalescer.Cleanup()

	var callCount int64

	queryFunc := func() ([]dns.RR, error) {
		atomic.AddInt64(&callCount, 1)
		rr, _ := dns.NewRR("example.com. 300 IN A 192.168.1.1")
		return []dns.RR{rr}, nil
	}

	const testDomain = "example.com."
	const testType = dns.TypeA
	key := QueryKey(testDomain, testType, "", false)

	// Execute two queries
	_, err1 := coalescer.CoalesceQuery(key, queryFunc)
	_, err2 := coalescer.CoalesceQuery(key, queryFunc)

	if err1 != nil || err2 != nil {
		t.Errorf("Queries failed: %v, %v", err1, err2)
	}

	// With coalescing disabled, both queries should execute
	actualCallCount := atomic.LoadInt64(&callCount)
	if actualCallCount != 2 {
		t.Errorf("Expected query function to be called twice, but was called %d times", actualCallCount)
	}
}

func TestQueryKeyGeneration(t *testing.T) {
	testCases := []struct {
		domain              string
		qtype               uint16
		clientIP            string
		enableClientRouting bool
		expected            string
	}{
		{"example.com.", dns.TypeA, "192.168.1.1", false, "example.com.:1"},
		{"example.com.", dns.TypeA, "192.168.1.1", true, "example.com.:1:192.168.1.1"},
		{"google.com.", dns.TypeAAAA, "10.0.0.1", false, "google.com.:28"},
		{"google.com.", dns.TypeAAAA, "10.0.0.1", true, "google.com.:28:10.0.0.1"},
	}

	for _, tc := range testCases {
		result := QueryKey(tc.domain, tc.qtype, tc.clientIP, tc.enableClientRouting)
		if result != tc.expected {
			t.Errorf("QueryKey(%s, %d, %s, %v) = %s, expected %s",
				tc.domain, tc.qtype, tc.clientIP, tc.enableClientRouting, result, tc.expected)
		}
	}
}

func TestQueryCoalescingTimeout(t *testing.T) {
	// Create config with short timeout
	cfg := &config.Config{
		EnableQueryCoalescing:          true,
		QueryCoalescingTimeout:         100 * time.Millisecond,
		QueryCoalescingCleanupInterval: 1 * time.Minute,
	}

	// Create test metrics recorder
	metrics := metric.NewFastMetricsRecorder()
	defer metrics.Close()

	// Create query coalescer
	coalescer := NewQueryCoalescer(cfg, metrics)
	defer coalescer.Cleanup()

	var callCount int64

	// Query function that takes longer than the timeout
	queryFunc := func() ([]dns.RR, error) {
		atomic.AddInt64(&callCount, 1)
		time.Sleep(200 * time.Millisecond) // Longer than timeout
		rr, _ := dns.NewRR("example.com. 300 IN A 192.168.1.1")
		return []dns.RR{rr}, nil
	}

	const testDomain = "example.com."
	const testType = dns.TypeA
	key := QueryKey(testDomain, testType, "", false)

	// This should timeout and fallback to direct execution
	start := time.Now()
	answers, err := coalescer.CoalesceQuery(key, queryFunc)
	duration := time.Since(start)

	// The first query should either succeed (if executed directly due to timeout)
	// or return results from the slow query
	if err != nil {
		t.Errorf("Query failed: %v", err)
	}

	// Should have at least one answer
	if len(answers) == 0 {
		t.Error("Expected at least one answer")
	}

	// The duration should be reasonable (not too much longer than timeout + execution time)
	if duration > 500*time.Millisecond {
		t.Errorf("Query took too long: %v", duration)
	}

	t.Logf("Query completed in %v with %d call(s)", duration, atomic.LoadInt64(&callCount))
}
