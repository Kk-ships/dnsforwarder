package metric

import (
	"sync"
	"testing"
)

// TestThreadSafeInitialization verifies that the global FastMetricsRecorder instance
// is initialized safely when accessed concurrently from multiple goroutines
func TestThreadSafeInitialization(t *testing.T) {
	// Reset the global instance for testing
	fastMetricsInstance = nil
	fastMetricsOnce = sync.Once{}

	const numGoroutines = 100
	var wg sync.WaitGroup
	results := make([]*FastMetricsRecorder, numGoroutines)

	// Launch multiple goroutines that try to get the instance simultaneously
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = GetFastMetricsInstance()
		}(i)
	}

	wg.Wait()

	// Verify all goroutines got the same instance
	firstInstance := results[0]
	if firstInstance == nil {
		t.Fatal("GetFastMetricsInstance() returned nil")
	}

	for i := 1; i < numGoroutines; i++ {
		if results[i] != firstInstance {
			t.Errorf("Got different instances: results[0]=%p, results[%d]=%p", firstInstance, i, results[i])
		}
	}

	// Clean up
	if firstInstance != nil {
		if err := firstInstance.Close(); err != nil {
			t.Errorf("Failed to close metrics instance: %v", err)
		}
	}
}

// TestGetFastMetricsInstanceReturnsNonNil verifies basic functionality
func TestGetFastMetricsInstanceReturnsNonNil(t *testing.T) {
	instance := GetFastMetricsInstance()
	if instance == nil {
		t.Fatal("GetFastMetricsInstance() returned nil")
	}

	// Verify we can call methods on the instance
	instance.FastRecordCacheHit()
	instance.FastRecordCacheMiss()

	// Clean up
	if err := instance.Close(); err != nil {
		t.Errorf("Failed to close metrics instance: %v", err)
	}
}
