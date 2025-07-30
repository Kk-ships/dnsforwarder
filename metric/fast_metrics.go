package metric

import (
	"dnsloadbalancer/util"
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
}

type metricUpdate struct {
	Type     uint8
	Value    float64
	Duration time.Duration
	Labels   [2]string // Pre-allocated for common case (type, status)
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

	f := &FastMetricsRecorder{
		metricUpdates: make(chan metricUpdate, channelSize),
	}

	// Start background processor for batched updates
	go f.processBatchedUpdates(batchSize, batchDelay)

	return f
}

// FastRecordDNSQuery records a DNS query with minimal allocations
func (f *FastMetricsRecorder) FastRecordDNSQuery(queryType, status string, duration time.Duration) {
	atomic.AddUint64(&f.dnsQueriesCount, 1)

	// Non-blocking send to batch processor
	select {
	case f.metricUpdates <- metricUpdate{
		Type:     MetricTypeDNSQuery,
		Duration: duration,
		Labels:   [2]string{queryType, status},
	}:
	default:
		// Channel full, skip to avoid blocking DNS responses
	}
}

// FastRecordCacheHit records a cache hit with atomic increment
func (f *FastMetricsRecorder) FastRecordCacheHit() {
	atomic.AddUint64(&f.cacheHitsCount, 1)

	select {
	case f.metricUpdates <- metricUpdate{Type: MetricTypeCacheHit}:
	default:
	}
}

// FastRecordCacheMiss records a cache miss with atomic increment
func (f *FastMetricsRecorder) FastRecordCacheMiss() {
	atomic.AddUint64(&f.cacheMissesCount, 1)

	select {
	case f.metricUpdates <- metricUpdate{Type: MetricTypeCacheMiss}:
	default:
	}
}

// FastRecordUpstreamQuery records an upstream DNS query with minimal allocations
func (f *FastMetricsRecorder) FastRecordUpstreamQuery(server, status string, duration time.Duration) {
	// Non-blocking send to batch processor
	select {
	case f.metricUpdates <- metricUpdate{
		Type:     MetricTypeUpstreamQuery,
		Duration: duration,
		Labels:   [2]string{server, status},
	}:
	default:
		// Channel full, skip to avoid blocking DNS responses
	}
}

// FastSetUpstreamServerReachable sets upstream server reachability status
func (f *FastMetricsRecorder) FastSetUpstreamServerReachable(server string, reachable bool) {
	value := float64(0)
	if reachable {
		value = 1
	}

	// Non-blocking send to batch processor
	select {
	case f.metricUpdates <- metricUpdate{
		Type:   MetricTypeUpstreamServerReachable,
		Value:  value,
		Labels: [2]string{server, ""},
	}:
	default:
		// Channel full, skip to avoid blocking
	}
}

// FastSetUpstreamServersTotal sets the total number of upstream servers
func (f *FastMetricsRecorder) FastSetUpstreamServersTotal(total int) {
	// Non-blocking send to batch processor
	select {
	case f.metricUpdates <- metricUpdate{
		Type:  MetricTypeUpstreamServersTotal,
		Value: float64(total),
	}:
	default:
		// Channel full, skip to avoid blocking
	}
}

// FastUpdateCacheSize updates the cache size metric
func (f *FastMetricsRecorder) FastUpdateCacheSize(size int) {
	// Non-blocking send to batch processor
	select {
	case f.metricUpdates <- metricUpdate{
		Type:  MetricTypeCacheSize,
		Value: float64(size),
	}:
	default:
		// Channel full, skip to avoid blocking
	}
}

// FastRecordError records an error with atomic increment
func (f *FastMetricsRecorder) FastRecordError(errorType, source string) {
	atomic.AddUint64(&f.errorsCount, 1)

	select {
	case f.metricUpdates <- metricUpdate{
		Type:   MetricTypeError,
		Labels: [2]string{errorType, source},
	}:
	default:
	}
}

// processBatchedUpdates processes metric updates in batches to reduce Prometheus contention
func (f *FastMetricsRecorder) processBatchedUpdates(batchSize int, batchDelay time.Duration) {
	ticker := time.NewTicker(batchDelay)
	defer ticker.Stop()

	updates := make([]metricUpdate, 0, batchSize) // Pre-allocate slice

	for {
		select {
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
			dnsQueriesTotal.WithLabelValues(update.Labels[0], update.Labels[1]).Inc()
			dnsQueryDuration.WithLabelValues(update.Labels[0], update.Labels[1]).Observe(update.Duration.Seconds())

		case MetricTypeCacheHit:
			cacheHitsTotal.Inc()

		case MetricTypeCacheMiss:
			cacheMissesTotal.Inc()

		case MetricTypeError:
			errorsTotal.WithLabelValues(update.Labels[0], update.Labels[1]).Inc()

		case MetricTypeUpstreamQuery:
			upstreamQueriesTotal.WithLabelValues(update.Labels[0], update.Labels[1]).Inc()
			upstreamQueryDuration.WithLabelValues(update.Labels[0], update.Labels[1]).Observe(update.Duration.Seconds())

		case MetricTypeCacheSize:
			cacheSize.Set(update.Value)

		case MetricTypeUpstreamServerReachable:
			upstreamServersReachable.WithLabelValues(update.Labels[0]).Set(update.Value)

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

// Global instance for fast metrics
var FastMetricsInstance = NewFastMetricsRecorder()
