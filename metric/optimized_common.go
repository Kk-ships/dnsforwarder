package metric

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Common metric update factory functions to reduce redundancy
func createMetricUpdate(metricType uint8, value float64, duration time.Duration, labels ...string) metricUpdate {
	return metricUpdate{
		Type:     metricType,
		Value:    value,
		Duration: duration,
		Labels:   labels,
	}
}

// Single function for all increment patterns
func createCounterUpdate(metricType uint8, labels ...string) metricUpdate {
	return createMetricUpdate(metricType, 0, 0, labels...)
}

// Single function for all gauge patterns
func createGaugeUpdate(metricType uint8, value float64, labels ...string) metricUpdate {
	return createMetricUpdate(metricType, value, 0, labels...)
}

// Combined function for both counter and histogram (common pattern)
func createCounterHistogramUpdate(metricType uint8, duration time.Duration, labels ...string) metricUpdate {
	return createMetricUpdate(metricType, 0, duration, labels...)
}

// Metric label requirements - centralized configuration
var metricLabelRequirements = map[uint8]int{
	MetricTypeDNSQuery:                2, // queryType, status
	MetricTypeError:                   2, // errorType, source
	MetricTypeUpstreamQuery:           2, // server, status
	MetricTypeUpstreamServerReachable: 1, // server
	MetricTypeDeviceIPDNSQuery:        1, // device_ip
	MetricTypeDomainQuery:             2, // domain, status
	MetricTypeDomainHit:               1, // domain
	MetricTypeCacheHit:                0, // no labels
	MetricTypeCacheMiss:               0, // no labels
	MetricTypeCacheSize:               0, // no labels
	MetricTypeUpstreamServersTotal:    0, // no labels
}

// Optimized validation using lookup table
func validateLabelsOptimized(metricType uint8, labels []string) bool {
	if required, exists := metricLabelRequirements[metricType]; exists {
		return len(labels) >= required
	}
	return true // Unknown metric types are allowed for extensibility
}

// Generic atomic increment for sync.Map with counter pattern
// Optimized to reduce allocations in the common case
func atomicIncrement(m *sync.Map, key string) uint64 {
	val, loaded := m.Load(key)
	if loaded {
		// Fast path: key exists, just increment
		return atomic.AddUint64(val.(*uint64), 1)
	}

	// Slow path: allocate new counter
	newCounter := new(uint64)
	*newCounter = 1
	val, loaded = m.LoadOrStore(key, newCounter)
	if loaded {
		// Race: another goroutine created the counter, use theirs
		return atomic.AddUint64(val.(*uint64), 1)
	}
	// We successfully stored the new counter with value 1
	return 1
}

// Generic atomic get for sync.Map with counter pattern
func atomicGet(m *sync.Map, key string) uint64 {
	val, ok := m.Load(key)
	if !ok {
		return 0
	}
	return atomic.LoadUint64(val.(*uint64))
}

// Generic function to get all counts from a sync.Map
// Pre-sizes the map hint for better performance when size is known
func getAllCounts(m *sync.Map) map[string]uint64 {
	// First pass: count entries for map sizing
	count := 0
	m.Range(func(key, value any) bool {
		count++
		return true
	})

	// Pre-allocate map with exact size to avoid rehashing
	result := make(map[string]uint64, count)
	m.Range(func(key, value any) bool {
		result[key.(string)] = atomic.LoadUint64(value.(*uint64))
		return true
	})
	return result
}

// Generic sorting function for count-based data
type CountItem struct {
	Key   string
	Count uint64
}

func getTopItems(m *sync.Map, n int) []CountItem {
	var items []CountItem
	m.Range(func(key, value any) bool {
		items = append(items, CountItem{
			Key:   key.(string),
			Count: atomic.LoadUint64(value.(*uint64)),
		})
		return true
	})

	// Sort by count in descending order
	sort.Slice(items, func(i, j int) bool {
		return items[i].Count > items[j].Count
	})

	// Return top N
	if n > len(items) {
		n = len(items)
	}
	return items[:n]
}

// System metrics cache with generic interface
// Uses atomic operations for lock-free reads when cache is fresh
type SystemMetricsCache struct {
	mu                  sync.Mutex
	lastUpdateNanos     int64 // Use atomic int64 for lock-free timestamp checks
	cacheDurationNanos  int64
	cachedGoroutines    int32  // Use int32 for atomic operations (atomic.LoadInt32/StoreInt32)
	cachedMemoryUsage   uint64 // Use atomic operations (atomic.LoadUint64/StoreUint64)
	getGoroutineCountFn func() int
	getMemoryUsageFn    func() uint64
}

func NewSystemMetricsCache(cacheDuration time.Duration,
	getGoroutines func() int,
	getMemory func() uint64) *SystemMetricsCache {
	cache := &SystemMetricsCache{
		cacheDurationNanos:  int64(cacheDuration),
		getGoroutineCountFn: getGoroutines,
		getMemoryUsageFn:    getMemory,
	}
	// Initialize the cache immediately to avoid returning 0 values
	cache.updateCacheLocked()
	return cache
}

// updateCacheLocked updates both goroutine and memory metrics together
// Caller must hold the mutex
func (s *SystemMetricsCache) updateCacheLocked() {
	// Use atomic stores to ensure visibility across goroutines
	atomic.StoreInt32(&s.cachedGoroutines, int32(s.getGoroutineCountFn()))
	atomic.StoreUint64(&s.cachedMemoryUsage, s.getMemoryUsageFn())
	// Store timestamp last to establish happens-before relationship
	atomic.StoreInt64(&s.lastUpdateNanos, time.Now().UnixNano())
}

// isCacheFresh checks if the cache is still valid using atomic operation (lock-free)
func (s *SystemMetricsCache) isCacheFresh() bool {
	lastUpdate := atomic.LoadInt64(&s.lastUpdateNanos)
	return time.Now().UnixNano()-lastUpdate < s.cacheDurationNanos
}

func (s *SystemMetricsCache) GetGoroutineCount() int {
	// Fast path: lock-free check if cache is fresh
	if s.isCacheFresh() {
		// Use atomic load for thread-safe reading
		return int(atomic.LoadInt32(&s.cachedGoroutines))
	}

	// Slow path: need to update cache
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring lock
	if s.isCacheFresh() {
		return int(atomic.LoadInt32(&s.cachedGoroutines))
	}

	// Update both metrics together to keep them in sync
	s.updateCacheLocked()
	return int(atomic.LoadInt32(&s.cachedGoroutines))
}

func (s *SystemMetricsCache) GetMemoryUsage() uint64 {
	// Fast path: lock-free check if cache is fresh
	if s.isCacheFresh() {
		// Use atomic load for thread-safe reading (critical on 32-bit systems)
		return atomic.LoadUint64(&s.cachedMemoryUsage)
	}

	// Slow path: need to update cache
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring lock
	if s.isCacheFresh() {
		return atomic.LoadUint64(&s.cachedMemoryUsage)
	}

	// Update both metrics together to keep them in sync
	s.updateCacheLocked()
	return atomic.LoadUint64(&s.cachedMemoryUsage)
}

// GetBothMetrics returns both metrics in a single call, reducing lock overhead
func (s *SystemMetricsCache) GetBothMetrics() (goroutines int, memoryUsage uint64) {
	// Fast path: lock-free check if cache is fresh
	if s.isCacheFresh() {
		// Use atomic loads for thread-safe reading
		return int(atomic.LoadInt32(&s.cachedGoroutines)), atomic.LoadUint64(&s.cachedMemoryUsage)
	}

	// Slow path: need to update cache
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring lock
	if !s.isCacheFresh() {
		s.updateCacheLocked()
	}

	// Use atomic loads even with lock held for consistency
	return int(atomic.LoadInt32(&s.cachedGoroutines)), atomic.LoadUint64(&s.cachedMemoryUsage)
}

// Process individual metric update - centralized processing logic
// Optimized with direct metric access and reduced label validations
func processMetricUpdate(update metricUpdate) {
	labels := update.Labels

	switch update.Type {
	case MetricTypeDNSQuery:
		// DNS queries need both counter and histogram
		if len(labels) >= 2 {
			metric := dnsQueriesTotal.WithLabelValues(labels[0], labels[1])
			metric.Inc()
			dnsQueryDuration.WithLabelValues(labels[0], labels[1]).Observe(update.Duration.Seconds())
		}
	case MetricTypeCacheHit:
		cacheHitsTotal.Inc()
	case MetricTypeCacheMiss:
		cacheMissesTotal.Inc()
	case MetricTypeError:
		if len(labels) >= 2 {
			errorsTotal.WithLabelValues(labels[0], labels[1]).Inc()
		}
	case MetricTypeUpstreamQuery:
		// Upstream queries need both counter and histogram
		if len(labels) >= 2 {
			upstreamQueriesTotal.WithLabelValues(labels[0], labels[1]).Inc()
			upstreamQueryDuration.WithLabelValues(labels[0], labels[1]).Observe(update.Duration.Seconds())
		}
	case MetricTypeCacheSize:
		cacheSize.Set(update.Value)
	case MetricTypeUpstreamServerReachable:
		if len(labels) >= 1 {
			upstreamServersReachable.WithLabelValues(labels[0]).Set(update.Value)
		}
	case MetricTypeUpstreamServersTotal:
		upstreamServersTotal.Set(update.Value)
	case MetricTypeDeviceIPDNSQuery:
		if len(labels) >= 1 {
			deviceIPDNSQueries.WithLabelValues(labels[0]).Set(update.Value)
		}
	case MetricTypeDomainQuery:
		if len(labels) >= 2 {
			domainQueriesTotal.WithLabelValues(labels[0], labels[1]).Inc()
		}
	case MetricTypeDomainHit:
		if len(labels) >= 1 {
			domainHitsTotal.WithLabelValues(labels[0]).Set(update.Value)
		}
	}
}

// ProcessMetricUpdatesBatch processes multiple metric updates efficiently
// Reduces overhead by batching Prometheus operations
func ProcessMetricUpdatesBatch(updates []metricUpdate) {
	for i := range updates {
		processMetricUpdate(updates[i])
	}
}
