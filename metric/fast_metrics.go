package metric

import (
	"context"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/util"
	"sync"
	"sync/atomic"
	"time"
)

// FastMetricsRecorder provides a high-performance metric recording interface
// that minimizes allocations and lock contention for DNS query hot paths
type FastMetricsRecorder struct {
	// Use atomic counters for frequently updated metrics to avoid locks
	dnsQueriesCount  uint64
	cacheHitsCount   uint64
	cacheMissesCount uint64
	errorsCount      uint64

	// Device IP specific DNS query counts
	deviceIPCounts sync.Map // map[string]*uint64

	// Domain specific DNS query counts and hit tracking
	domainQueryCounts sync.Map // map[string]*uint64
	domainHitCounts   sync.Map // map[string]*uint64

	// Channel for batched metric updates to reduce Prometheus contention
	metricUpdates chan metricUpdate

	// Shutdown coordination
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup

	// Drain configuration for shutdown behavior
	drainTimeout    time.Duration
	maxDrainUpdates int
	drainBatchSize  int
}

type metricUpdate struct {
	Type     uint8
	Value    float64
	Duration time.Duration
	Labels   []string // Flexible label slice to accommodate varying label requirements
}

const (
	MetricTypeDNSQuery = iota
	MetricTypeCacheHit
	MetricTypeCacheMiss
	MetricTypeError
	MetricTypeUpstreamQuery
	MetricTypeCacheSize
	MetricTypeUpstreamServerReachable
	MetricTypeUpstreamServersTotal
	MetricTypeDeviceIPDNSQuery
	MetricTypeDomainQuery
	MetricTypeDomainHit
)

func NewFastMetricsRecorder() *FastMetricsRecorder {
	batchSize := util.GetEnvInt("METRICS_BATCH_SIZE", 500)
	batchDelay := util.GetEnvDuration("METRICS_BATCH_DELAY", 100*time.Millisecond)
	channelSize := util.GetEnvInt("METRICS_CHANNEL_SIZE", 10000)

	// Configurable drain behavior during shutdown
	drainTimeout := util.GetEnvDuration("METRICS_DRAIN_TIMEOUT", 5*time.Second)
	maxDrainUpdates := util.GetEnvInt("METRICS_MAX_DRAIN_UPDATES", 1000)
	drainBatchSize := util.GetEnvInt("METRICS_DRAIN_BATCH_SIZE", 50)

	ctx, cancel := context.WithCancel(context.Background())

	f := &FastMetricsRecorder{
		metricUpdates:   make(chan metricUpdate, channelSize),
		ctx:             ctx,
		cancel:          cancel,
		done:            make(chan struct{}),
		drainTimeout:    drainTimeout,
		maxDrainUpdates: maxDrainUpdates,
		drainBatchSize:  drainBatchSize,
	}

	// Start background processor for batched updates
	f.wg.Add(1)
	go f.processBatchedUpdates(batchSize, batchDelay)

	return f
}

// Helper functions for creating metric updates with different label counts

// validateLabels uses the optimized validation from common functions
func validateLabels(metricType uint8, labels []string) bool {
	return validateLabelsOptimized(metricType, labels)
}

// NewMetricUpdate creates a metric update with flexible labels
func NewMetricUpdate(metricType uint8, labels ...string) metricUpdate {
	return createCounterUpdate(metricType, labels...)
}

// NewMetricUpdateWithValue creates a metric update with a value and flexible labels
func NewMetricUpdateWithValue(metricType uint8, value float64, labels ...string) metricUpdate {
	return createGaugeUpdate(metricType, value, labels...)
}

// NewMetricUpdateWithDuration creates a metric update with a duration and flexible labels
func NewMetricUpdateWithDuration(metricType uint8, duration time.Duration, labels ...string) metricUpdate {
	return createHistogramUpdate(metricType, duration, labels...)
}

// Specific helper functions for common metric patterns using optimized functions

// NewDNSQueryUpdate creates a DNS query metric update with required labels
func NewDNSQueryUpdate(queryType, status string, duration time.Duration) metricUpdate {
	return createCounterHistogramUpdate(MetricTypeDNSQuery, duration, queryType, status)
}

// NewUpstreamQueryUpdate creates an upstream query metric update with required labels
func NewUpstreamQueryUpdate(server, status string, duration time.Duration) metricUpdate {
	return createCounterHistogramUpdate(MetricTypeUpstreamQuery, duration, server, status)
}

// NewErrorUpdate creates an error metric update with required labels
func NewErrorUpdate(errorType, source string) metricUpdate {
	return createCounterUpdate(MetricTypeError, errorType, source)
}

// NewUpstreamServerReachableUpdate creates an upstream server reachable metric update
func NewUpstreamServerReachableUpdate(server string, reachable bool) metricUpdate {
	value := float64(0)
	if reachable {
		value = 1
	}
	return createGaugeUpdate(MetricTypeUpstreamServerReachable, value, server)
}

// NewCacheSizeUpdate creates a cache size metric update
func NewCacheSizeUpdate(size int) metricUpdate {
	return createGaugeUpdate(MetricTypeCacheSize, float64(size))
}

// NewUpstreamServersTotalUpdate creates an upstream servers total metric update
func NewUpstreamServersTotalUpdate(total int) metricUpdate {
	return createGaugeUpdate(MetricTypeUpstreamServersTotal, float64(total))
}

// NewDeviceIPDNSQueryUpdate creates a device IP DNS query metric update
func NewDeviceIPDNSQueryUpdate(deviceIP string, count uint64) metricUpdate {
	return createGaugeUpdate(MetricTypeDeviceIPDNSQuery, float64(count), deviceIP)
}

// NewDomainQueryUpdate creates a domain query metric update
func NewDomainQueryUpdate(domain, status string) metricUpdate {
	return createCounterUpdate(MetricTypeDomainQuery, domain, status)
}

// NewDomainHitUpdate creates a domain hit metric update
func NewDomainHitUpdate(domain string, hitCount uint64) metricUpdate {
	return createGaugeUpdate(MetricTypeDomainHit, float64(hitCount), domain)
}

// ConditionalTimer provides efficient timing that only works when metrics are enabled
type ConditionalTimer struct {
	start   time.Time
	enabled bool
}

// StartConditionalTimer starts timing only if metrics are enabled, avoiding overhead when disabled
func StartConditionalTimer(enabled bool) ConditionalTimer {
	if enabled {
		return ConditionalTimer{start: time.Now(), enabled: true}
	}
	return ConditionalTimer{enabled: false}
}

// Elapsed returns the duration since the timer started, or 0 if metrics were disabled
func (ct ConditionalTimer) Elapsed() time.Duration {
	if ct.enabled {
		return time.Since(ct.start)
	}
	return 0
}

// IsEnabled returns whether timing is active
func (ct ConditionalTimer) IsEnabled() bool {
	return ct.enabled
}

// Global conditional timer helpers for common patterns

// StartDNSQueryTimer starts a timer specifically for DNS query metrics
func StartDNSQueryTimer(metricsEnabled bool) ConditionalTimer {
	return StartConditionalTimer(metricsEnabled)
}

// StartCacheTimer starts a timer specifically for cache operation metrics
func StartCacheTimer(metricsEnabled bool) ConditionalTimer {
	return StartConditionalTimer(metricsEnabled)
}

// incrementDeviceIPCount atomically increments the DNS query count for a specific device IP
func (f *FastMetricsRecorder) incrementDeviceIPCount(deviceIP string) {
	atomicIncrement(&f.deviceIPCounts, deviceIP)
}

// incrementDomainQueryCount atomically increments the DNS query count for a specific domain
func (f *FastMetricsRecorder) incrementDomainQueryCount(domain string) {
	atomicIncrement(&f.domainQueryCounts, domain)
}

// incrementDomainHitCount atomically increments the hit count for a specific domain
// and returns the new count value
func (f *FastMetricsRecorder) incrementDomainHitCount(domain string) uint64 {
	return atomicIncrement(&f.domainHitCounts, domain)
}

// FastRecordDNSQuery records a DNS query with minimal allocations
func (f *FastMetricsRecorder) FastRecordDNSQuery(queryType, status string, duration time.Duration) {
	f.FastRecordDNSQueryWithDeviceIP(queryType, status, "", duration)
}

// FastRecordDNSQueryWithDeviceIP records a DNS query with device IP tracking
func (f *FastMetricsRecorder) FastRecordDNSQueryWithDeviceIP(queryType, status, deviceIP string, duration time.Duration) {
	f.FastRecordDNSQueryWithDeviceIPAndDomain(queryType, status, deviceIP, "", duration)
}

// FastRecordDNSQueryWithDeviceIPAndDomain records a DNS query with device IP and domain tracking
func (f *FastMetricsRecorder) FastRecordDNSQueryWithDeviceIPAndDomain(queryType, status, deviceIP, domain string, duration time.Duration) {
	atomic.AddUint64(&f.dnsQueriesCount, 1)

	// Track device IP specific count if provided
	if deviceIP != "" {
		f.incrementDeviceIPCount(deviceIP)
	}

	// Track domain specific count if provided
	if domain != "" {
		f.incrementDomainQueryCount(domain)
		// Send domain query update
		select {
		case f.metricUpdates <- NewDomainQueryUpdate(domain, status):
		default:
			// Channel full, skip to avoid blocking DNS responses
		}
	}

	// Non-blocking send to batch processor
	select {
	case f.metricUpdates <- NewDNSQueryUpdate(queryType, status, duration):
	default:
		// Channel full, skip to avoid blocking DNS responses
	}
}

// FastRecordCacheHit records a cache hit with atomic increment
func (f *FastMetricsRecorder) FastRecordCacheHit() {
	atomic.AddUint64(&f.cacheHitsCount, 1)

	select {
	case f.metricUpdates <- NewMetricUpdate(MetricTypeCacheHit):
	default:
	}
}

// FastRecordCacheMiss records a cache miss with atomic increment
func (f *FastMetricsRecorder) FastRecordCacheMiss() {
	atomic.AddUint64(&f.cacheMissesCount, 1)

	select {
	case f.metricUpdates <- NewMetricUpdate(MetricTypeCacheMiss):
	default:
	}
}

// FastRecordUpstreamQuery records an upstream DNS query with minimal allocations
func (f *FastMetricsRecorder) FastRecordUpstreamQuery(server, status string, duration time.Duration) {
	// Non-blocking send to batch processor
	select {
	case f.metricUpdates <- NewUpstreamQueryUpdate(server, status, duration):
	default:
		// Channel full, skip to avoid blocking DNS responses
	}
}

// FastSetUpstreamServerReachable sets upstream server reachability status
func (f *FastMetricsRecorder) FastSetUpstreamServerReachable(server string, reachable bool) {
	// Non-blocking send to batch processor
	select {
	case f.metricUpdates <- NewUpstreamServerReachableUpdate(server, reachable):
	default:
		// Channel full, skip to avoid blocking
	}
}

// FastSetUpstreamServersTotal sets the total number of upstream servers
func (f *FastMetricsRecorder) FastSetUpstreamServersTotal(total int) {
	// Non-blocking send to batch processor
	select {
	case f.metricUpdates <- NewUpstreamServersTotalUpdate(total):
	default:
		// Channel full, skip to avoid blocking
	}
}

// FastUpdateCacheSize updates the cache size metric
func (f *FastMetricsRecorder) FastUpdateCacheSize(size int) {
	// Non-blocking send to batch processor
	select {
	case f.metricUpdates <- NewCacheSizeUpdate(size):
	default:
		// Channel full, skip to avoid blocking
	}
}

// FastRecordError records an error with atomic increment
func (f *FastMetricsRecorder) FastRecordError(errorType, source string) {
	atomic.AddUint64(&f.errorsCount, 1)

	select {
	case f.metricUpdates <- NewErrorUpdate(errorType, source):
	default:
	}
}

// FastRecordCustomMetric allows recording metrics with arbitrary label combinations
// This demonstrates the full flexibility of the new design
func (f *FastMetricsRecorder) FastRecordCustomMetric(metricType uint8, labels ...string) {
	if !validateLabels(metricType, labels) {
		logutil.Logger.Warnf("Invalid labels for metric type %d: %v", metricType, labels)
		return
	}

	select {
	case f.metricUpdates <- NewMetricUpdate(metricType, labels...):
	default:
	}
}

// FastUpdateDeviceIPMetrics sends device IP DNS query counts to Prometheus
func (f *FastMetricsRecorder) FastUpdateDeviceIPMetrics() {
	deviceIPCounts := f.GetAllDeviceIPCounts()
	for ip, count := range deviceIPCounts {
		select {
		case f.metricUpdates <- NewDeviceIPDNSQueryUpdate(ip, count):
		default:
			// Channel full, skip to avoid blocking
		}
	}
}

// FastRecordDomainHit records a domain hit
func (f *FastMetricsRecorder) FastRecordDomainHit(domain string) {
	newCount := f.incrementDomainHitCount(domain)

	// Send domain hit update to batch processor
	select {
	case f.metricUpdates <- NewDomainHitUpdate(domain, newCount):
	default:
		// Channel full, skip to avoid blocking
	}
}

// FastUpdateDomainMetrics sends domain metrics to Prometheus
func (f *FastMetricsRecorder) FastUpdateDomainMetrics() {
	domainHitCounts := f.GetAllDomainHitCounts()
	for domain, count := range domainHitCounts {
		select {
		case f.metricUpdates <- NewDomainHitUpdate(domain, count):
		default:
			// Channel full, skip to avoid blocking
		}
	}
}

// processBatchedUpdates processes metric updates in batches to reduce Prometheus contention
func (f *FastMetricsRecorder) processBatchedUpdates(batchSize int, batchDelay time.Duration) {
	defer f.wg.Done()
	defer close(f.done)

	ticker := time.NewTicker(batchDelay)
	defer ticker.Stop()

	// Device IP metrics update interval (every 10 batch intervals)
	deviceIPUpdateCounter := 0
	deviceIPUpdateInterval := 10

	updates := make([]metricUpdate, 0, batchSize) // Pre-allocate slice

	for {
		select {
		case <-f.ctx.Done():
			// Graceful shutdown: flush any remaining updates before stopping
			if len(updates) > 0 {
				f.flushUpdates(updates)
			}
			// Drain the channel to avoid goroutine leaks
			f.drainMetricUpdates()
			return

		case update := <-f.metricUpdates:
			updates = append(updates, update)

		case <-ticker.C:
			if len(updates) > 0 {
				f.flushUpdates(updates)
				updates = updates[:0] // Reset slice but keep capacity
			}

			// Periodically update device IP and domain metrics
			deviceIPUpdateCounter++
			if deviceIPUpdateCounter >= deviceIPUpdateInterval {
				f.FastUpdateDeviceIPMetrics()
				f.FastUpdateDomainMetrics()
				deviceIPUpdateCounter = 0
			}
		}

		// If buffer is getting full, flush immediately
		if len(updates) >= batchSize {
			f.flushUpdates(updates)
			updates = updates[:0]
		}
	}
}

// flushUpdates applies batched updates to Prometheus metrics using centralized processing
func (f *FastMetricsRecorder) flushUpdates(updates []metricUpdate) {
	for _, update := range updates {
		processMetricUpdate(update)
	}
}

// RecordError implements the interface for compatibility
func (f *FastMetricsRecorder) RecordError(errorType, source string) {
	f.FastRecordError(errorType, source)
}

// RecordUpstreamQuery implements the interface for compatibility
func (f *FastMetricsRecorder) RecordUpstreamQuery(server, status string, duration time.Duration) {
	f.FastRecordUpstreamQuery(server, status, duration)
}

// SetUpstreamServerReachable implements the interface for compatibility
func (f *FastMetricsRecorder) SetUpstreamServerReachable(server string, reachable bool) {
	f.FastSetUpstreamServerReachable(server, reachable)
}

// SetUpstreamServersTotal implements the interface for compatibility
func (f *FastMetricsRecorder) SetUpstreamServersTotal(total int) {
	f.FastSetUpstreamServersTotal(total)
}

// GetStats returns the current atomic counter values for monitoring
func (f *FastMetricsRecorder) GetStats() (dnsQueries, cacheHits, cacheMisses, errors uint64) {
	return atomic.LoadUint64(&f.dnsQueriesCount),
		atomic.LoadUint64(&f.cacheHitsCount),
		atomic.LoadUint64(&f.cacheMissesCount),
		atomic.LoadUint64(&f.errorsCount)
}

// GetDeviceIPCount returns the DNS query count for a specific device IP
func (f *FastMetricsRecorder) GetDeviceIPCount(deviceIP string) uint64 {
	return atomicGet(&f.deviceIPCounts, deviceIP)
}

// GetAllDeviceIPCounts returns a map of all device IPs and their DNS query counts
func (f *FastMetricsRecorder) GetAllDeviceIPCounts() map[string]uint64 {
	return getAllCounts(&f.deviceIPCounts)
}

// GetDomainQueryCount returns the DNS query count for a specific domain
func (f *FastMetricsRecorder) GetDomainQueryCount(domain string) uint64 {
	return atomicGet(&f.domainQueryCounts, domain)
}

// GetAllDomainQueryCounts returns a map of all domains and their DNS query counts
func (f *FastMetricsRecorder) GetAllDomainQueryCounts() map[string]uint64 {
	return getAllCounts(&f.domainQueryCounts)
}

// GetDomainHitCount returns the hit count for a specific domain
func (f *FastMetricsRecorder) GetDomainHitCount(domain string) uint64 {
	return atomicGet(&f.domainHitCounts, domain)
}

// GetAllDomainHitCounts returns a map of all domains and their hit counts
func (f *FastMetricsRecorder) GetAllDomainHitCounts() map[string]uint64 {
	return getAllCounts(&f.domainHitCounts)
}

// GetTopDeviceIPs returns the top N device IPs by query count
func (f *FastMetricsRecorder) GetTopDeviceIPs(n int) []struct {
	IP    string
	Count uint64
} {
	items := getTopItems(&f.deviceIPCounts, n)
	result := make([]struct {
		IP    string
		Count uint64
	}, len(items))

	for i, item := range items {
		result[i].IP = item.Key
		result[i].Count = item.Count
	}
	return result
}

// GetTopDomainsByQueries returns the top N domains by query count
func (f *FastMetricsRecorder) GetTopDomainsByQueries(n int) []struct {
	Domain string
	Count  uint64
} {
	items := getTopItems(&f.domainQueryCounts, n)
	result := make([]struct {
		Domain string
		Count  uint64
	}, len(items))

	for i, item := range items {
		result[i].Domain = item.Key
		result[i].Count = item.Count
	}
	return result
}

// GetTopDomainsByHits returns the top N domains by hit count
func (f *FastMetricsRecorder) GetTopDomainsByHits(n int) []struct {
	Domain string
	Count  uint64
} {
	items := getTopItems(&f.domainHitCounts, n)
	result := make([]struct {
		Domain string
		Count  uint64
	}, len(items))

	for i, item := range items {
		result[i].Domain = item.Key
		result[i].Count = item.Count
	}
	return result
}

// drainMetricUpdates drains any remaining updates from the channel during shutdown
// with timeout and batch size limits to prevent hanging
func (f *FastMetricsRecorder) drainMetricUpdates() {
	deadline := time.Now().Add(f.drainTimeout)
	processedCount := 0
	pendingUpdates := make([]metricUpdate, 0, f.drainBatchSize)

	for time.Now().Before(deadline) && processedCount < f.maxDrainUpdates {
		select {
		case update := <-f.metricUpdates:
			pendingUpdates = append(pendingUpdates, update)
			processedCount++

			// Process in smaller batches to avoid blocking too long
			if len(pendingUpdates) >= f.drainBatchSize {
				f.flushUpdates(pendingUpdates)
				pendingUpdates = pendingUpdates[:0] // Reset but keep capacity
			}
		default:
			// Channel is empty, flush any remaining updates and exit
			if len(pendingUpdates) > 0 {
				f.flushUpdates(pendingUpdates)
			}
			return
		}
	}

	// Flush any remaining updates before timeout/limit
	if len(pendingUpdates) > 0 {
		f.flushUpdates(pendingUpdates)
	}

	// Log if we hit limits (useful for monitoring/debugging)
	if processedCount >= f.maxDrainUpdates {
		// In production, you might want to use a proper logger here
		// For now, we'll just document this behavior
		_ = processedCount // Avoid unused variable warning
	}
}

// Shutdown initiates graceful shutdown of the metrics recorder
func (f *FastMetricsRecorder) Shutdown(timeout time.Duration) error {
	// Signal shutdown
	f.cancel()

	// Wait for graceful shutdown with timeout
	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

// Close implements io.Closer interface and provides immediate shutdown
func (f *FastMetricsRecorder) Close() error {
	f.cancel()
	f.wg.Wait()
	return nil
}

// WaitForShutdown waits until the background processor has completely stopped
func (f *FastMetricsRecorder) WaitForShutdown() {
	<-f.done
}

// Global instance for fast metrics - uses lazy initialization to avoid race conditions
var (
	fastMetricsInstance *FastMetricsRecorder
	fastMetricsOnce     sync.Once
)

// GetFastMetricsInstance returns the global FastMetricsRecorder instance,
// initializing it lazily in a thread-safe manner
func GetFastMetricsInstance() *FastMetricsRecorder {
	fastMetricsOnce.Do(func() {
		fastMetricsInstance = NewFastMetricsRecorder()
	})
	return fastMetricsInstance
}

// ShutdownGlobalInstance provides a convenient way to shutdown the global metrics instance
func ShutdownGlobalInstance(timeout time.Duration) error {
	instance := GetFastMetricsInstance()
	return instance.Shutdown(timeout)
}

// CloseGlobalInstance provides a convenient way to immediately close the global metrics instance
func CloseGlobalInstance() error {
	instance := GetFastMetricsInstance()
	return instance.Close()
}
