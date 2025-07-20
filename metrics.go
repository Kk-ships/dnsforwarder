package main

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prometheus metrics
var (
	// DNS query metrics
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

	// Cache metrics
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

	// Upstream server metrics
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

	// System metrics
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

	// Error metrics
	errorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dns_errors_total",
			Help: "Total number of DNS errors",
		},
		[]string{"type", "source"},
	)
)

// Metrics aggregates for efficient atomic operations
var (
	metricsUpdateInterval = getEnvDuration("METRICS_UPDATE_INTERVAL", 30*time.Second)
	totalQueries          int64
	totalCacheHits        int64
	totalCacheMisses      int64
	totalUpstreamQueries  int64
	totalErrors           int64
)

func init() {
	// Register all metrics with Prometheus
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

// MetricsRecorder provides methods to record various metrics
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

// Global metrics recorder instance
var metricsRecorder = NewMetricsRecorder()

// StartMetricsUpdater starts a goroutine that periodically updates system metrics
func StartMetricsUpdater() {
	startTime := time.Now()

	go func() {
		ticker := time.NewTicker(metricsUpdateInterval)
		defer ticker.Stop()

		for range ticker.C {
			// Update uptime
			serverUptime.Add(metricsUpdateInterval.Seconds())

			// Update system metrics
			updateSystemMetrics()

			// Update cache size
			if dnsCache != nil {
				items := dnsCache.Items()
				metricsRecorder.UpdateCacheSize(len(items))
			}

			// Log metrics summary periodically
			logMetricsSummary()
		}
	}()

	logWithBufferf("Metrics updater started, update interval: %v", metricsUpdateInterval)
	logWithBufferf("Server start time: %v", startTime)
}

// StartMetricsServer starts the Prometheus metrics HTTP server
func StartMetricsServer() {
	metricsPort := getEnvString("METRICS_PORT", ":8080")
	metricsPath := getEnvString("METRICS_PATH", "/metrics")

	mux := http.NewServeMux()
	mux.Handle(metricsPath, promhttp.Handler())

	// Add a health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Add a simple status endpoint
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
		logWithBufferf("Starting Prometheus metrics server on %s%s", metricsPort, metricsPath)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logWithBufferf("[ERROR] Metrics server failed to start: %v", err)
		}
	}()
}

// updateSystemMetrics updates system-level metrics
func updateSystemMetrics() {
	// Update goroutine count
	goroutineCount.Set(float64(getGoroutineCount()))

	// Update memory usage
	memoryUsage.Set(float64(getMemoryUsage()))
}

// logMetricsSummary logs a summary of key metrics
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

	logWithBufferf("[METRICS] Queries: %d, Cache hits: %d (%.2f%%), Upstream: %d, Errors: %d",
		queries, hits, hitRate, upstreamQueries, errors)
}
