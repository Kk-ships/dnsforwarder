package metric

import (
	"context"
	"testing"
	"time"
)

func TestFlexibleLabels(t *testing.T) {
	recorder := NewFastMetricsRecorder()

	// Test DNS query with standard 2 labels
	recorder.FastRecordDNSQuery("A", "success", 100*time.Millisecond)

	// Test DNS query with extra labels (demonstrates flexibility)
	recorder.FastRecordDNSQueryWithExtraLabels("A", "success", "192.168.1.1", "us-west", 100*time.Millisecond)

	// Test upstream server reachable with single label (no empty string placeholders)
	recorder.FastSetUpstreamServerReachable("8.8.8.8", true)

	// Test error recording with 2 labels
	recorder.FastRecordError("timeout", "upstream")

	// Test cache operations (no labels)
	recorder.FastRecordCacheHit()
	recorder.FastRecordCacheMiss()

	// Test metrics with values (no labels)
	recorder.FastUpdateCacheSize(1000)
	recorder.FastSetUpstreamServersTotal(3)

	// Verify atomic counters
	dnsQueries, cacheHits, cacheMisses, errors := recorder.GetStats()

	if dnsQueries != 2 {
		t.Errorf("Expected 2 DNS queries, got %d", dnsQueries)
	}

	if cacheHits != 1 {
		t.Errorf("Expected 1 cache hit, got %d", cacheHits)
	}

	if cacheMisses != 1 {
		t.Errorf("Expected 1 cache miss, got %d", cacheMisses)
	}

	if errors != 1 {
		t.Errorf("Expected 1 error, got %d", errors)
	}
}

func TestLabelValidation(t *testing.T) {
	// Test validation function
	tests := []struct {
		metricType    uint8
		labels        []string
		shouldBeValid bool
	}{
		{MetricTypeDNSQuery, []string{"A", "success"}, true},
		{MetricTypeDNSQuery, []string{"A"}, false}, // Missing status
		{MetricTypeError, []string{"timeout", "upstream"}, true},
		{MetricTypeError, []string{"timeout"}, false}, // Missing source
		{MetricTypeUpstreamServerReachable, []string{"8.8.8.8"}, true},
		{MetricTypeUpstreamServerReachable, []string{}, false}, // Missing server
		{MetricTypeCacheHit, []string{}, true},                 // No labels needed
		{MetricTypeCacheSize, []string{}, true},                // No labels needed
	}

	for _, test := range tests {
		result := validateLabels(test.metricType, test.labels)
		if result != test.shouldBeValid {
			t.Errorf("validateLabels(%d, %v) = %v, expected %v",
				test.metricType, test.labels, result, test.shouldBeValid)
		}
	}
}

func TestSpecificHelperFunctions(t *testing.T) {
	// Test that helper functions create correct metric updates

	// DNS Query
	dnsUpdate := NewDNSQueryUpdate("A", "success", 100*time.Millisecond)
	if dnsUpdate.Type != MetricTypeDNSQuery {
		t.Errorf("Expected MetricTypeDNSQuery, got %d", dnsUpdate.Type)
	}
	if len(dnsUpdate.Labels) != 2 || dnsUpdate.Labels[0] != "A" || dnsUpdate.Labels[1] != "success" {
		t.Errorf("Expected labels [A, success], got %v", dnsUpdate.Labels)
	}

	// Upstream server reachable
	reachableUpdate := NewUpstreamServerReachableUpdate("8.8.8.8", true)
	if reachableUpdate.Type != MetricTypeUpstreamServerReachable {
		t.Errorf("Expected MetricTypeUpstreamServerReachable, got %d", reachableUpdate.Type)
	}
	if len(reachableUpdate.Labels) != 1 || reachableUpdate.Labels[0] != "8.8.8.8" {
		t.Errorf("Expected labels [8.8.8.8], got %v", reachableUpdate.Labels)
	}
	if reachableUpdate.Value != 1.0 {
		t.Errorf("Expected value 1.0 for reachable=true, got %f", reachableUpdate.Value)
	}

	// Cache size
	cacheSizeUpdate := NewCacheSizeUpdate(1000)
	if cacheSizeUpdate.Type != MetricTypeCacheSize {
		t.Errorf("Expected MetricTypeCacheSize, got %d", cacheSizeUpdate.Type)
	}
	if len(cacheSizeUpdate.Labels) != 0 {
		t.Errorf("Expected no labels for cache size, got %v", cacheSizeUpdate.Labels)
	}
	if cacheSizeUpdate.Value != 1000.0 {
		t.Errorf("Expected value 1000.0, got %f", cacheSizeUpdate.Value)
	}
}

func TestGracefulShutdown(t *testing.T) {
	recorder := NewFastMetricsRecorder()

	// Record some metrics
	recorder.FastRecordDNSQuery("A", "success", 100*time.Millisecond)
	recorder.FastRecordCacheHit()
	recorder.FastSetUpstreamServerReachable("8.8.8.8", true)

	// Test graceful shutdown with timeout
	err := recorder.Shutdown(5 * time.Second)
	if err != nil {
		t.Errorf("Expected successful shutdown, got error: %v", err)
	}

	// Verify the background processor has stopped
	select {
	case <-recorder.done:
		// Good, the processor has stopped
	case <-time.After(1 * time.Second):
		t.Error("Background processor did not stop after shutdown")
	}
}

func TestShutdownTimeout(t *testing.T) {
	recorder := NewFastMetricsRecorder()

	// Test shutdown with very short timeout (this should still succeed quickly)
	err := recorder.Shutdown(1 * time.Nanosecond)
	if err != nil && err != context.DeadlineExceeded {
		t.Errorf("Expected nil or DeadlineExceeded, got: %v", err)
	}

	// Clean up
	if closeErr := recorder.Close(); closeErr != nil {
		t.Errorf("Failed to close recorder: %v", closeErr)
	}
}

func TestCloseMethod(t *testing.T) {
	recorder := NewFastMetricsRecorder()

	// Record some metrics
	recorder.FastRecordDNSQuery("A", "success", 100*time.Millisecond)

	// Test immediate close
	err := recorder.Close()
	if err != nil {
		t.Errorf("Expected successful close, got error: %v", err)
	}

	// Verify the background processor has stopped
	select {
	case <-recorder.done:
		// Good, the processor has stopped
	case <-time.After(1 * time.Second):
		t.Error("Background processor did not stop after close")
	}
}

func TestMetricsAfterShutdown(t *testing.T) {
	recorder := NewFastMetricsRecorder()

	// Shutdown the recorder
	err := recorder.Shutdown(5 * time.Second)
	if err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Try to record metrics after shutdown (should not panic or block)
	recorder.FastRecordDNSQuery("A", "success", 100*time.Millisecond)
	recorder.FastRecordCacheHit()

	// This should complete without hanging or panicking
	// The metrics may or may not be processed, but the calls should be safe
}

func TestConditionalTimer(t *testing.T) {
	// Test enabled timer
	enabledTimer := StartConditionalTimer(true)
	if !enabledTimer.IsEnabled() {
		t.Error("Expected timer to be enabled")
	}

	// Wait a bit to ensure measurable duration
	time.Sleep(10 * time.Millisecond)
	duration := enabledTimer.Elapsed()
	if duration <= 0 {
		t.Error("Expected positive duration from enabled timer")
	}
	if duration < 5*time.Millisecond {
		t.Error("Expected duration to be at least 5ms")
	}

	// Test disabled timer
	disabledTimer := StartConditionalTimer(false)
	if disabledTimer.IsEnabled() {
		t.Error("Expected timer to be disabled")
	}

	time.Sleep(10 * time.Millisecond)
	duration = disabledTimer.Elapsed()
	if duration != 0 {
		t.Errorf("Expected 0 duration from disabled timer, got %v", duration)
	}
}

func TestSpecificTimers(t *testing.T) {
	// Test DNS query timer
	dnsTimer := StartDNSQueryTimer(true)
	if !dnsTimer.IsEnabled() {
		t.Error("Expected DNS timer to be enabled")
	}

	// Test cache timer
	cacheTimer := StartCacheTimer(false)
	if cacheTimer.IsEnabled() {
		t.Error("Expected cache timer to be disabled")
	}
}

func BenchmarkConditionalTimerEnabled(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		timer := StartConditionalTimer(true)
		_ = timer.Elapsed()
	}
}

func BenchmarkConditionalTimerDisabled(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		timer := StartConditionalTimer(false)
		_ = timer.Elapsed()
	}
}

func BenchmarkTimeNowDirect(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_ = time.Since(start)
	}
}
