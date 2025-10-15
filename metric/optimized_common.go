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
func atomicIncrement(m *sync.Map, key string) uint64 {
	val, loaded := m.Load(key)
	if !loaded {
		var zero uint64 = 0
		ptr := &zero
		val, _ = m.LoadOrStore(key, ptr)
	}
	return atomic.AddUint64(val.(*uint64), 1)
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
func getAllCounts(m *sync.Map) map[string]uint64 {
	result := make(map[string]uint64)
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
type SystemMetricsCache struct {
	mu                  sync.RWMutex
	lastUpdate          time.Time
	cacheDuration       time.Duration
	cachedGoroutines    int
	cachedMemoryUsage   uint64
	getGoroutineCountFn func() int
	getMemoryUsageFn    func() uint64
}

func NewSystemMetricsCache(cacheDuration time.Duration,
	getGoroutines func() int,
	getMemory func() uint64) *SystemMetricsCache {
	cache := &SystemMetricsCache{
		cacheDuration:       cacheDuration,
		getGoroutineCountFn: getGoroutines,
		getMemoryUsageFn:    getMemory,
	}
	// Initialize the cache immediately to avoid returning 0 values
	cache.updateCache()
	return cache
}

// updateCache updates both goroutine and memory metrics together
func (s *SystemMetricsCache) updateCache() {
	s.cachedGoroutines = s.getGoroutineCountFn()
	s.cachedMemoryUsage = s.getMemoryUsageFn()
	s.lastUpdate = time.Now()
}

func (s *SystemMetricsCache) GetGoroutineCount() int {
	s.mu.RLock()
	if time.Since(s.lastUpdate) < s.cacheDuration {
		count := s.cachedGoroutines
		s.mu.RUnlock()
		return count
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if time.Since(s.lastUpdate) < s.cacheDuration {
		return s.cachedGoroutines
	}

	// Update both metrics together to keep them in sync
	s.updateCache()
	return s.cachedGoroutines
}

func (s *SystemMetricsCache) GetMemoryUsage() uint64 {
	s.mu.RLock()
	if time.Since(s.lastUpdate) < s.cacheDuration {
		usage := s.cachedMemoryUsage
		s.mu.RUnlock()
		return usage
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if time.Since(s.lastUpdate) < s.cacheDuration {
		return s.cachedMemoryUsage
	}

	// Update both metrics together to keep them in sync
	s.updateCache()
	return s.cachedMemoryUsage
}

// Process individual metric update - centralized processing logic
func processMetricUpdate(update metricUpdate) {
	switch update.Type {
	case MetricTypeDNSQuery:
		if len(update.Labels) >= 2 {
			dnsQueriesTotal.WithLabelValues(update.Labels[0], update.Labels[1]).Inc()
			dnsQueryDuration.WithLabelValues(update.Labels[0], update.Labels[1]).Observe(update.Duration.Seconds())
		}
	case MetricTypeCacheHit:
		cacheHitsTotal.Inc()
	case MetricTypeCacheMiss:
		cacheMissesTotal.Inc()
	case MetricTypeError:
		if len(update.Labels) >= 2 {
			errorsTotal.WithLabelValues(update.Labels[0], update.Labels[1]).Inc()
		}
	case MetricTypeUpstreamQuery:
		if len(update.Labels) >= 2 {
			upstreamQueriesTotal.WithLabelValues(update.Labels[0], update.Labels[1]).Inc()
			upstreamQueryDuration.WithLabelValues(update.Labels[0], update.Labels[1]).Observe(update.Duration.Seconds())
		}
	case MetricTypeCacheSize:
		cacheSize.Set(update.Value)
	case MetricTypeUpstreamServerReachable:
		if len(update.Labels) >= 1 {
			upstreamServersReachable.WithLabelValues(update.Labels[0]).Set(update.Value)
		}
	case MetricTypeUpstreamServersTotal:
		upstreamServersTotal.Set(update.Value)
	case MetricTypeDeviceIPDNSQuery:
		if len(update.Labels) >= 1 {
			deviceIPDNSQueries.WithLabelValues(update.Labels[0]).Set(update.Value)
		}
	case MetricTypeDomainQuery:
		if len(update.Labels) >= 2 {
			domainQueriesTotal.WithLabelValues(update.Labels[0], update.Labels[1]).Inc()
		}
	case MetricTypeDomainHit:
		if len(update.Labels) >= 1 {
			domainHitsTotal.WithLabelValues(update.Labels[0]).Set(update.Value)
		}
	}
}
