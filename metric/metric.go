package metric

import (
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/util"
	"net/http"
	"runtime"
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
	// Device IP metrics
	deviceIPDNSQueries = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_device_ip_queries_total",
			Help: "Total number of DNS queries by device IP",
		},
		[]string{"device_ip"},
	)
	// Domain metrics
	domainQueriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dns_domain_queries_total",
			Help: "Total number of DNS queries by domain",
		},
		[]string{"domain", "status"},
	)
	domainHitsTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dns_domain_hits_total",
			Help: "Total number of cache hits by domain",
		},
		[]string{"domain"},
	)
)

var (
	metricsUpdateInterval time.Duration
	// Pre-allocated HTTP response strings to avoid allocations
	healthResponse = []byte("OK")
	statusResponse = []byte(`{"status":"running","service":"dns-forwarder"}`)
	// Optimized system metrics cache
	systemMetricsCache *SystemMetricsCache
)

func init() {
	metricsUpdateInterval = util.GetEnvDuration("METRICS_UPDATE_INTERVAL", 30*time.Second)

	// Initialize system metrics cache with optimized functions
	systemMetricsCache = NewSystemMetricsCache(
		5*time.Second, // Cache duration
		func() int { return runtime.NumGoroutine() },
		func() uint64 {
			// Use optimized memory stats with pooling
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			return m.Alloc
		},
	)

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
		deviceIPDNSQueries,
		domainQueriesTotal,
		domainHitsTotal,
	)
}

type MetricsRecorder struct{}

func NewMetricsRecorder() *MetricsRecorder {
	return &MetricsRecorder{}
}

func (m *MetricsRecorder) RecordDNSQuery(queryType, status string, duration time.Duration) {
	dnsQueriesTotal.WithLabelValues(queryType, status).Inc()
	dnsQueryDuration.WithLabelValues(queryType, status).Observe(duration.Seconds())
}

func (m *MetricsRecorder) RecordCacheHit() {
	cacheHitsTotal.Inc()
}

func (m *MetricsRecorder) RecordCacheMiss() {
	cacheMissesTotal.Inc()
}

func (m *MetricsRecorder) RecordUpstreamQuery(server, status string, duration time.Duration) {
	upstreamQueriesTotal.WithLabelValues(server, status).Inc()
	upstreamQueryDuration.WithLabelValues(server, status).Observe(duration.Seconds())
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
}

func (m *MetricsRecorder) RecordDomainQuery(domain, status string) {
	domainQueriesTotal.WithLabelValues(domain, status).Inc()
}

func (m *MetricsRecorder) RecordDomainHit(domain string, hitCount uint64) {
	domainHitsTotal.WithLabelValues(domain).Set(float64(hitCount))
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
		}
	}()
	logutil.Logger.Infof("Metrics updater started, update interval: %v", metricsUpdateInterval)
	logutil.Logger.Infof("Server start time: %v", startTime)
}

func StartMetricsServer() {
	metricsPort := util.GetEnvString("METRICS_PORT", ":8080")
	metricsPath := util.GetEnvString("METRICS_PATH", "/metrics")
	mux := http.NewServeMux()
	mux.Handle(metricsPath, promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(healthResponse); err != nil {
			logutil.Logger.Errorf("Failed to write health check response: %v", err)
		}
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(statusResponse); err != nil {
			logutil.Logger.Errorf("Failed to write status response: %v", err)
		}
	})
	server := &http.Server{
		Addr:    metricsPort,
		Handler: mux,
	}
	go func() {
		logutil.Logger.Infof("Starting Prometheus metrics server on %s%s", metricsPort, metricsPath)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logutil.Logger.Errorf("Metrics server failed to start: %v", err)
		}
	}()
}

// GetGoroutineCount returns the current number of goroutines using optimized cache
func GetGoroutineCount() int {
	return systemMetricsCache.GetGoroutineCount()
}

// GetMemoryUsage returns the current memory usage in bytes using optimized cache
func GetMemoryUsage() uint64 {
	return systemMetricsCache.GetMemoryUsage()
}

func updateSystemMetrics() {
	goroutineCount.Set(float64(GetGoroutineCount()))
	memoryUsage.Set(float64(GetMemoryUsage()))

	// Update device IP DNS query metrics from FastMetricsRecorder
	if fastMetricsInstance != nil {
		deviceIPCounts := fastMetricsInstance.GetAllDeviceIPCounts()
		UpdateDeviceIPDNSMetrics(deviceIPCounts)

		// Update domain metrics
		domainHitCounts := fastMetricsInstance.GetAllDomainHitCounts()
		UpdateDomainHitMetrics(domainHitCounts)
	}
}

// UpdateDeviceIPDNSMetrics updates Prometheus metrics for device IP DNS query counts
func UpdateDeviceIPDNSMetrics(deviceIPCounts map[string]uint64) {
	for ip, count := range deviceIPCounts {
		deviceIPDNSQueries.WithLabelValues(ip).Set(float64(count))
	}
}

// UpdateDomainHitMetrics updates Prometheus metrics for domain hit counts
func UpdateDomainHitMetrics(domainHitCounts map[string]uint64) {
	for domain, count := range domainHitCounts {
		domainHitsTotal.WithLabelValues(domain).Set(float64(count))
	}
}
