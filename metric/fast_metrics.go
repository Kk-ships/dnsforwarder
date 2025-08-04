package metric

import (
	"context"
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

	// Channel for batched metric updates to reduce Prometheus contention
	metricUpdates chan metricUpdate

	// Shutdown coordination
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup
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
)

func NewFastMetricsRecorder() *FastMetricsRecorder {
	batchSize := util.GetEnvInt("METRICS_BATCH_SIZE", 500)
	batchDelay := util.GetEnvDuration("METRICS_BATCH_DELAY", 100*time.Millisecond)
	channelSize := util.GetEnvInt("METRICS_CHANNEL_SIZE", 10000)

	ctx, cancel := context.WithCancel(context.Background())

	f := &FastMetricsRecorder{
		metricUpdates: make(chan metricUpdate, channelSize),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	// Start background processor for batched updates
	f.wg.Add(1)
	go f.processBatchedUpdates(batchSize, batchDelay)

	return f
}

// Helper functions for creating metric updates with different label counts

// validateLabels checks if the provided labels match the expected count for a metric type
func validateLabels(metricType uint8, labels []string) bool {
	switch metricType {
	case MetricTypeDNSQuery:
		return len(labels) >= 2 // Requires: queryType, status (may have additional labels)
	case MetricTypeError:
		return len(labels) >= 2 // Requires: errorType, source
	case MetricTypeUpstreamQuery:
		return len(labels) >= 2 // Requires: server, status
	case MetricTypeUpstreamServerReachable:
		return len(labels) >= 1 // Requires: server
	case MetricTypeCacheHit, MetricTypeCacheMiss, MetricTypeCacheSize, MetricTypeUpstreamServersTotal:
		return true // No labels required
	default:
		return true // Unknown metric types are allowed (for future extensibility)
	}
}

// NewMetricUpdate creates a metric update with flexible labels
func NewMetricUpdate(metricType uint8, labels ...string) metricUpdate {
	return metricUpdate{
		Type:   metricType,
		Labels: labels,
	}
}

// NewMetricUpdateWithValue creates a metric update with a value and flexible labels
func NewMetricUpdateWithValue(metricType uint8, value float64, labels ...string) metricUpdate {
	return metricUpdate{
		Type:   metricType,
		Value:  value,
		Labels: labels,
	}
}

// NewMetricUpdateWithDuration creates a metric update with a duration and flexible labels
func NewMetricUpdateWithDuration(metricType uint8, duration time.Duration, labels ...string) metricUpdate {
	return metricUpdate{
		Type:     metricType,
		Duration: duration,
		Labels:   labels,
	}
}

// Specific helper functions for common metric patterns to make intent clear

// NewDNSQueryUpdate creates a DNS query metric update with required labels
func NewDNSQueryUpdate(queryType, status string, duration time.Duration) metricUpdate {
	return NewMetricUpdateWithDuration(MetricTypeDNSQuery, duration, queryType, status)
}

// NewUpstreamQueryUpdate creates an upstream query metric update with required labels
func NewUpstreamQueryUpdate(server, status string, duration time.Duration) metricUpdate {
	return NewMetricUpdateWithDuration(MetricTypeUpstreamQuery, duration, server, status)
}

// NewErrorUpdate creates an error metric update with required labels
func NewErrorUpdate(errorType, source string) metricUpdate {
	return NewMetricUpdate(MetricTypeError, errorType, source)
}

// NewUpstreamServerReachableUpdate creates an upstream server reachable metric update
func NewUpstreamServerReachableUpdate(server string, reachable bool) metricUpdate {
	value := float64(0)
	if reachable {
		value = 1
	}
	return NewMetricUpdateWithValue(MetricTypeUpstreamServerReachable, value, server)
}

// NewCacheSizeUpdate creates a cache size metric update
func NewCacheSizeUpdate(size int) metricUpdate {
	return NewMetricUpdateWithValue(MetricTypeCacheSize, float64(size))
}

// NewUpstreamServersTotalUpdate creates an upstream servers total metric update
func NewUpstreamServersTotalUpdate(total int) metricUpdate {
	return NewMetricUpdateWithValue(MetricTypeUpstreamServersTotal, float64(total))
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

// FastRecordDNSQuery records a DNS query with minimal allocations
func (f *FastMetricsRecorder) FastRecordDNSQuery(queryType, status string, duration time.Duration) {
	atomic.AddUint64(&f.dnsQueriesCount, 1)

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

// FastRecordDNSQueryWithExtraLabels demonstrates flexibility with more than 2 labels
// This could be used for future metrics that need additional dimensions
func (f *FastMetricsRecorder) FastRecordDNSQueryWithExtraLabels(queryType, status, clientIP, region string, duration time.Duration) {
	atomic.AddUint64(&f.dnsQueriesCount, 1)

	// Non-blocking send to batch processor with 4 labels
	// Note: Current Prometheus metrics only use first 2 labels, but this shows flexibility
	select {
	case f.metricUpdates <- NewMetricUpdateWithDuration(MetricTypeDNSQuery, duration, queryType, status, clientIP, region):
	default:
		// Channel full, skip to avoid blocking DNS responses
	}
}

// FastRecordCustomMetric allows recording metrics with arbitrary label combinations
// This demonstrates the full flexibility of the new design
func (f *FastMetricsRecorder) FastRecordCustomMetric(metricType uint8, labels ...string) {
	if !validateLabels(metricType, labels) {
		// In a production system, you might want to log this or handle it differently
		return
	}

	select {
	case f.metricUpdates <- NewMetricUpdate(metricType, labels...):
	default:
	}
}

// processBatchedUpdates processes metric updates in batches to reduce Prometheus contention
func (f *FastMetricsRecorder) processBatchedUpdates(batchSize int, batchDelay time.Duration) {
	defer f.wg.Done()
	defer close(f.done)

	ticker := time.NewTicker(batchDelay)
	defer ticker.Stop()

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
		}

		// If buffer is getting full, flush immediately
		if len(updates) >= batchSize {
			f.flushUpdates(updates)
			updates = updates[:0]
		}
	}
}

// flushUpdates applies batched updates to Prometheus metrics
func (f *FastMetricsRecorder) flushUpdates(updates []metricUpdate) {
	for _, update := range updates {
		switch update.Type {
		case MetricTypeDNSQuery:
			// DNS queries require exactly 2 labels: queryType and status
			if len(update.Labels) >= 2 {
				// Use only the first 2 labels for Prometheus metrics that expect exactly 2
				dnsQueriesTotal.WithLabelValues(update.Labels[0], update.Labels[1]).Inc()
				dnsQueryDuration.WithLabelValues(update.Labels[0], update.Labels[1]).Observe(update.Duration.Seconds())
			}

		case MetricTypeCacheHit:
			cacheHitsTotal.Inc()

		case MetricTypeCacheMiss:
			cacheMissesTotal.Inc()

		case MetricTypeError:
			// Error metrics require exactly 2 labels: errorType and source
			if len(update.Labels) >= 2 {
				errorsTotal.WithLabelValues(update.Labels[0], update.Labels[1]).Inc()
			}

		case MetricTypeUpstreamQuery:
			// Upstream queries require exactly 2 labels: server and status
			if len(update.Labels) >= 2 {
				upstreamQueriesTotal.WithLabelValues(update.Labels[0], update.Labels[1]).Inc()
				upstreamQueryDuration.WithLabelValues(update.Labels[0], update.Labels[1]).Observe(update.Duration.Seconds())
			}

		case MetricTypeCacheSize:
			cacheSize.Set(update.Value)

		case MetricTypeUpstreamServerReachable:
			// Upstream server reachable requires exactly 1 label: server
			if len(update.Labels) >= 1 {
				upstreamServersReachable.WithLabelValues(update.Labels[0]).Set(update.Value)
			}

		case MetricTypeUpstreamServersTotal:
			upstreamServersTotal.Set(update.Value)
		}
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

// drainMetricUpdates drains any remaining updates from the channel during shutdown
func (f *FastMetricsRecorder) drainMetricUpdates() {
	for {
		select {
		case update := <-f.metricUpdates:
			// Process remaining updates during shutdown
			f.flushUpdates([]metricUpdate{update})
		default:
			// Channel is empty
			return
		}
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

// Global instance for fast metrics
var FastMetricsInstance = NewFastMetricsRecorder()

// ShutdownGlobalInstance provides a convenient way to shutdown the global metrics instance
func ShutdownGlobalInstance(timeout time.Duration) error {
	return FastMetricsInstance.Shutdown(timeout)
}

// CloseGlobalInstance provides a convenient way to immediately close the global metrics instance
func CloseGlobalInstance() error {
	return FastMetricsInstance.Close()
}
