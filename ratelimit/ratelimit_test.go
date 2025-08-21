package ratelimit

import (
	"dnsloadbalancer/metric"
	"sync"
	"testing"
	"time"
)

func TestDefaultRateLimitConfig(t *testing.T) {
	config := DefaultRateLimitConfig()

	if config.MaxRequestsPerSecond != 100 {
		t.Errorf("Expected MaxRequestsPerSecond=100, got %d", config.MaxRequestsPerSecond)
	}

	if config.MaxRequestsPerMinute != 3000 {
		t.Errorf("Expected MaxRequestsPerMinute=3000, got %d", config.MaxRequestsPerMinute)
	}

	if config.AdaptiveEnabled != true {
		t.Errorf("Expected AdaptiveEnabled=true, got %v", config.AdaptiveEnabled)
	}

	if config.BlockingEnabled != true {
		t.Errorf("Expected BlockingEnabled=true, got %v", config.BlockingEnabled)
	}
}

func TestNewRateLimiter(t *testing.T) {
	config := DefaultRateLimitConfig()
	metrics := metric.NewFastMetricsRecorder()

	rl := NewRateLimiter(config, metrics)
	defer rl.Shutdown()

	if rl.config != config {
		t.Error("Config not set correctly")
	}

	if rl.metrics != metrics {
		t.Error("Metrics not set correctly")
	}
}

func TestRateLimitBasic(t *testing.T) {
	config := DefaultRateLimitConfig()
	config.MaxRequestsPerSecond = 5 // Low limit for testing
	metrics := metric.NewFastMetricsRecorder()

	rl := NewRateLimiter(config, metrics)
	defer rl.Shutdown()

	clientIP := "192.168.1.100"

	// First 5 requests should be allowed
	for i := 0; i < 5; i++ {
		result := rl.CheckRateLimit(clientIP)
		if !result.Allowed {
			t.Errorf("Request %d should be allowed", i+1)
		}
	}

	// 6th request should be denied
	result := rl.CheckRateLimit(clientIP)
	if result.Allowed {
		t.Error("Request should be denied due to rate limit")
	}

	if result.Reason != "QPS limit exceeded" {
		t.Errorf("Expected 'QPS limit exceeded', got '%s'", result.Reason)
	}
}

func TestBurstDetection(t *testing.T) {
	config := DefaultRateLimitConfig()
	config.MaxRequestsPerSecond = 10
	config.BurstThreshold = 7 // Lower threshold than requests being sent
	metrics := metric.NewFastMetricsRecorder()

	rl := NewRateLimiter(config, metrics)
	defer rl.Shutdown()

	clientIP := "192.168.1.101"

	// Send 8 requests quickly to trigger burst detection (exceed threshold of 7)
	for i := 0; i < 8; i++ {
		result := rl.CheckRateLimit(clientIP)
		if !result.Allowed {
			t.Errorf("Request %d should be allowed", i+1)
		}
	}

	// Check client stats to see if burst was detected
	stats := rl.GetClientStats(clientIP)
	if stats["burst_count"].(int32) == 0 {
		t.Error("Burst should have been detected")
	}

	if stats["suspicion_level"].(int32) == 0 {
		t.Error("Suspicion level should have increased due to burst")
	}
}

func TestAdaptiveThrottling(t *testing.T) {
	config := DefaultRateLimitConfig()
	config.MaxRequestsPerSecond = 10
	config.AdaptiveEnabled = true
	config.SuspicionThreshold = 20
	config.ThrottleMultiplier = 0.5
	metrics := metric.NewFastMetricsRecorder()

	rl := NewRateLimiter(config, metrics)
	defer rl.Shutdown()

	clientIP := "192.168.1.102"

	// Get the client bucket and artificially increase suspicion
	bucketInterface, _ := rl.clients.LoadOrStore(clientIP, &ClientBucket{
		requests:   make([]int64, rl.config.WindowSlots),
		lastUpdate: time.Now(),
	})
	bucket := bucketInterface.(*ClientBucket)
	bucket.suspicionLevel = 30 // Above threshold

	// Now make requests - should hit throttled limit (5 instead of 10)
	allowedCount := 0
	for i := 0; i < 10; i++ {
		result := rl.CheckRateLimit(clientIP)
		if result.Allowed {
			allowedCount++
		}
	}

	if allowedCount > 5 {
		t.Errorf("Expected throttled rate (~5), got %d allowed requests", allowedCount)
	}
}

func TestClientBlocking(t *testing.T) {
	config := DefaultRateLimitConfig()
	config.BlockingEnabled = true
	config.BlockThreshold = 50
	config.BlockDuration = time.Second * 2
	metrics := metric.NewFastMetricsRecorder()

	rl := NewRateLimiter(config, metrics)
	defer rl.Shutdown()

	clientIP := "192.168.1.103"

	// Get the client bucket and set high suspicion level
	bucketInterface, _ := rl.clients.LoadOrStore(clientIP, &ClientBucket{
		requests:   make([]int64, rl.config.WindowSlots),
		lastUpdate: time.Now(),
	})
	bucket := bucketInterface.(*ClientBucket)
	bucket.suspicionLevel = 80 // Above block threshold

	// Make a request that should trigger blocking
	result := rl.CheckRateLimit(clientIP)
	if result.Allowed {
		t.Error("Request should trigger blocking")
	}

	// Verify client is blocked
	if !rl.IsBlocked(clientIP) {
		t.Error("Client should be blocked")
	}

	// Make another request - should be denied due to blocking
	result2 := rl.CheckRateLimit(clientIP)
	if result2.Allowed {
		t.Error("Blocked client request should be denied")
	}

	// Wait for block to expire
	time.Sleep(time.Second * 3)

	// Client should no longer be blocked
	if rl.IsBlocked(clientIP) {
		t.Error("Client should no longer be blocked")
	}
}

func TestManualUnblock(t *testing.T) {
	config := DefaultRateLimitConfig()
	config.BlockingEnabled = true
	config.BlockDuration = time.Hour // Long duration
	metrics := metric.NewFastMetricsRecorder()

	rl := NewRateLimiter(config, metrics)
	defer rl.Shutdown()

	clientIP := "192.168.1.104"

	// Get the client bucket and manually block
	bucketInterface, _ := rl.clients.LoadOrStore(clientIP, &ClientBucket{
		requests:   make([]int64, rl.config.WindowSlots),
		lastUpdate: time.Now(),
	})
	bucket := bucketInterface.(*ClientBucket)
	rl.blockClient(bucket, time.Now(), "test_block")

	// Verify client is blocked
	if !rl.IsBlocked(clientIP) {
		t.Error("Client should be blocked")
	}

	// Manually unblock
	rl.UnblockClient(clientIP)

	// Client should no longer be blocked
	if rl.IsBlocked(clientIP) {
		t.Error("Client should be unblocked after manual unblock")
	}
}

func TestConcurrentAccess(t *testing.T) {
	config := DefaultRateLimitConfig()
	config.MaxRequestsPerSecond = 100
	metrics := metric.NewFastMetricsRecorder()

	rl := NewRateLimiter(config, metrics)
	defer rl.Shutdown()

	var wg sync.WaitGroup
	numGoroutines := 10
	requestsPerGoroutine := 20

	// Test concurrent access from multiple goroutines
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()

			clientIP := "192.168.1.200" // Same client IP for all goroutines

			for j := 0; j < requestsPerGoroutine; j++ {
				result := rl.CheckRateLimit(clientIP)
				// Don't fail on rate limit - just ensure no race conditions
				_ = result
			}
		}(i)
	}

	wg.Wait()

	// Verify stats are consistent
	stats := rl.GetGlobalStats()
	totalRequests := stats["total_requests"].(uint64)
	expectedTotal := uint64(numGoroutines * requestsPerGoroutine)

	if totalRequests != expectedTotal {
		t.Errorf("Expected %d total requests, got %d", expectedTotal, totalRequests)
	}
}

func TestGetStats(t *testing.T) {
	config := DefaultRateLimitConfig()
	metrics := metric.NewFastMetricsRecorder()

	rl := NewRateLimiter(config, metrics)
	defer rl.Shutdown()

	clientIP := "192.168.1.105"

	// Make some requests
	for i := 0; i < 3; i++ {
		rl.CheckRateLimit(clientIP)
	}

	// Test client stats
	clientStats := rl.GetClientStats(clientIP)
	if !clientStats["exists"].(bool) {
		t.Error("Client should exist in stats")
	}

	if clientStats["total_requests"].(int64) != 3 {
		t.Errorf("Expected 3 total requests, got %d", clientStats["total_requests"])
	}

	// Test global stats
	globalStats := rl.GetGlobalStats()
	if globalStats["active_clients"].(int) != 1 {
		t.Errorf("Expected 1 active client, got %d", globalStats["active_clients"])
	}

	if globalStats["total_requests"].(uint64) < 3 {
		t.Errorf("Expected at least 3 total requests, got %d", globalStats["total_requests"])
	}
}

func TestSlidingWindow(t *testing.T) {
	config := DefaultRateLimitConfig()
	config.MaxRequestsPerSecond = 5
	config.WindowSlots = 5 // 5 slots for sliding window
	metrics := metric.NewFastMetricsRecorder()

	rl := NewRateLimiter(config, metrics)
	defer rl.Shutdown()

	clientIP := "192.168.1.106"

	// Fill up the rate limit for current second
	for i := 0; i < 5; i++ {
		result := rl.CheckRateLimit(clientIP)
		if !result.Allowed {
			t.Errorf("Request %d should be allowed", i+1)
		}
	}

	// Next request should be denied (hitting the QPS limit)
	result := rl.CheckRateLimit(clientIP)
	if result.Allowed {
		t.Error("Request should be denied")
	}

	// Wait for next second boundary
	time.Sleep(time.Second + 100*time.Millisecond)

	// Request should now be allowed (new second slot)
	result = rl.CheckRateLimit(clientIP)
	if !result.Allowed {
		t.Error("Request should be allowed in new second")
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	// This test verifies that the config loading function works
	config := LoadRateLimitConfigFromEnv()

	if config == nil {
		t.Error("Config should not be nil")
		return
	}

	// Test that defaults are applied
	if config.MaxRequestsPerSecond != 100 {
		t.Errorf("Expected default QPS=100, got %d", config.MaxRequestsPerSecond)
	}

	if config.WindowSlots != 60 {
		t.Errorf("Expected default WindowSlots=60, got %d", config.WindowSlots)
	}
}
