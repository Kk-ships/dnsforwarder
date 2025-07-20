package metric

import (
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/util"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prometheus metrics
var (
	dnsQueriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dns_queries_total",
			Help: "Total number of DNS queries processed",
		},
		[]string{"type", "status"},
	)
	dnsQueryDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "dns_query_duration_seconds",
			Help:    "Duration of DNS queries in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"type", "status"},
	)
	cacheHitsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "dns_cache_hits_total",
			Help: "Total number of DNS cache hits",
		},
	)
	cacheMissesTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "dns_cache_misses_total",
			Help: "Total number of DNS cache misses",
		},
	)
	cacheSize = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "dns_cache_size",
			Help: "Current number of entries in DNS cache",
		},
	)
	upstreamQueriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dns_upstream_queries_total",
			Help: "Total number of queries sent to upstream DNS servers",
		},
		[]string{"server", "status"},
	)
	upstreamQueryDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "dns_upstream_query_duration_seconds",
			Help:    "Duration of upstream DNS queries in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"server", "status"},
	)
	upstreamServersReachable = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_upstream_servers_reachable",
			Help: "Whether upstream DNS servers are reachable (1 for reachable, 0 for unreachable)",
		},
		[]string{"server"},
	)
	upstreamServersTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "dns_upstream_servers_total",
			Help: "Total number of configured upstream DNS servers",
		},
	)
	serverUptime = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "dns_server_uptime_seconds_total",
			Help: "Total uptime of the DNS server in seconds",
		},
	)
	memoryUsage = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "dns_server_memory_usage_bytes",
			Help: "Memory usage of the DNS server in bytes",
		},
	)
	goroutineCount = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "dns_server_goroutines",
			Help: "Number of goroutines currently running",
		},
	)
	errorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dns_errors_total",
			Help: "Total number of DNS errors",
		},
		[]string{"type", "source"},
	)
)

var (
	metricsUpdateInterval time.Duration
	totalQueries          int64
	totalCacheHits        int64
	totalCacheMisses      int64
	totalUpstreamQueries  int64
	totalErrors           int64
	dnsCacheMutex         sync.Mutex
)

func init() {
	metricsUpdateInterval = util.GetEnvDuration("METRICS_UPDATE_INTERVAL", 30*time.Second)
	prometheus.MustRegister(
		dnsQueriesTotal,
		dnsQueryDuration,
		cacheHitsTotal,
		cacheMissesTotal,
		cacheSize,
		upstreamQueriesTotal,
		upstreamQueryDuration,
		upstreamServersReachable,
		upstreamServersTotal,
		serverUptime,
		memoryUsage,
		goroutineCount,
		errorsTotal,
	)
}

type MetricsRecorder struct{}

func NewMetricsRecorder() *MetricsRecorder {
	return &MetricsRecorder{}
}

func (m *MetricsRecorder) RecordDNSQuery(queryType, status string, duration time.Duration) {
	dnsQueriesTotal.WithLabelValues(queryType, status).Inc()
	dnsQueryDuration.WithLabelValues(queryType, status).Observe(duration.Seconds())
	atomic.AddInt64(&totalQueries, 1)
}

func (m *MetricsRecorder) RecordCacheHit() {
	cacheHitsTotal.Inc()
	atomic.AddInt64(&totalCacheHits, 1)
}

func (m *MetricsRecorder) RecordCacheMiss() {
	cacheMissesTotal.Inc()
	atomic.AddInt64(&totalCacheMisses, 1)
}

func (m *MetricsRecorder) RecordUpstreamQuery(server, status string, duration time.Duration) {
	upstreamQueriesTotal.WithLabelValues(server, status).Inc()
	upstreamQueryDuration.WithLabelValues(server, status).Observe(duration.Seconds())
	atomic.AddInt64(&totalUpstreamQueries, 1)
}

func (m *MetricsRecorder) SetUpstreamServerReachable(server string, reachable bool) {
	value := float64(0)
	if reachable {
		value = 1
	}
	upstreamServersReachable.WithLabelValues(server).Set(value)
}

func (m *MetricsRecorder) SetUpstreamServersTotal(total int) {
	upstreamServersTotal.Set(float64(total))
}

func (m *MetricsRecorder) UpdateCacheSize(size int) {
	cacheSize.Set(float64(size))
}

func (m *MetricsRecorder) RecordError(errorType, source string) {
	errorsTotal.WithLabelValues(errorType, source).Inc()
	atomic.AddInt64(&totalErrors, 1)
}

var MetricsRecorderInstance = NewMetricsRecorder()

func StartMetricsUpdater() {
	startTime := time.Now()
	go func() {
		ticker := time.NewTicker(metricsUpdateInterval)
		defer ticker.Stop()
		for range ticker.C {
			serverUptime.Add(metricsUpdateInterval.Seconds())
			updateSystemMetrics()
			dnsCacheMutex.Lock()
			// The cache size update should be called from outside the metric package to avoid import cycles.
			dnsCacheMutex.Unlock()
			logMetricsSummary()
		}
	}()
	logutil.LogWithBufferf("Metrics updater started, update interval: %v", metricsUpdateInterval)
	logutil.LogWithBufferf("Server start time: %v", startTime)
}

func StartMetricsServer() {
	metricsPort := util.GetEnvString("METRICS_PORT", ":8080")
	metricsPath := util.GetEnvString("METRICS_PATH", "/metrics")
	mux := http.NewServeMux()
	mux.Handle(metricsPath, promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"running","service":"dns-forwarder"}`))
	})
	server := &http.Server{
		Addr:    metricsPort,
		Handler: mux,
	}
	go func() {
		logutil.LogWithBufferf("Starting Prometheus metrics server on %s%s", metricsPort, metricsPath)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logutil.LogWithBufferf("[ERROR] Metrics server failed to start: %v", err)
		}
	}()
}

// GetGoroutineCount returns the current number of goroutines
func GetGoroutineCount() int {
	return runtime.NumGoroutine()
}

// GetMemoryUsage returns the current memory usage in bytes
func GetMemoryUsage() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Alloc
}

func updateSystemMetrics() {
	goroutineCount.Set(float64(GetGoroutineCount()))
	memoryUsage.Set(float64(GetMemoryUsage()))
}

func logMetricsSummary() {
	queries := atomic.LoadInt64(&totalQueries)
	hits := atomic.LoadInt64(&totalCacheHits)
	misses := atomic.LoadInt64(&totalCacheMisses)
	upstreamQueries := atomic.LoadInt64(&totalUpstreamQueries)
	errors := atomic.LoadInt64(&totalErrors)
	hitRate := float64(0)
	if hits+misses > 0 {
		hitRate = float64(hits) / float64(hits+misses) * 100
	}
	logutil.LogWithBufferf("[METRICS] Queries: %d, Cache hits: %d (%.2f%%), Upstream: %d, Errors: %d",
		queries, hits, hitRate, upstreamQueries, errors)
}
