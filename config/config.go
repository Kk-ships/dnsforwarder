package config

import (
	"dnsloadbalancer/util"
	"fmt"
	"sync"
	"time"
)

// DNS Response Constants
const (
	ARecordInvalidAnswer       = "0.0.0.0"
	AAAARecordInvalidAnswer    = "::"
	NegativeResponseTTLDivisor = 4
	MinCacheTTL                = 30 * time.Second
	MaxCacheTTL                = 24 * time.Hour
	DefaultPublicDNS           = "1.1.1.1:53"
)

// Config holds all application configuration
type Config struct {
	// DNS Server Configuration
	DNSPort    string
	UDPSize    int
	DNSTimeout time.Duration

	// DNS Servers Configuration
	PrivateServers []string
	PublicServers  []string
	CacheRefresh   time.Duration
	WorkerCount    int
	TestDomain     string
	DNSStatslog    time.Duration

	// Cache Configuration
	CacheSize int
	CacheTTL  time.Duration

	// Metrics Configuration
	MetricsPort   string
	EnableMetrics bool
	// Fast metrics are always enabled for optimal performance
	// These configurations remain for compatibility and future tuning
	MetricsBatchSize  int           // Batch size for metric updates
	MetricsBatchDelay time.Duration // Delay between metric batches

	// Client Routing Configuration
	PublicOnlyClients    []string
	PublicOnlyClientMACs []string
	EnableClientRouting  bool

	// Domain Routing Configuration
	EnableDomainRouting              bool
	DomainRoutingFolder              string
	DomainRoutingTableReloadInterval int

	// Logging Configuration
	LogLevel string

	// Cache Persistence Configuration
	EnableCachePersistence   bool
	CachePersistenceFile     string
	CachePersistenceInterval time.Duration
	CachePersistenceMaxAge   time.Duration

	// Rate Limiting Configuration
	EnableRateLimit bool
	// Basic rate limiting - default to generous limits
	MaxRequestsPerSecond int
	MaxRequestsPerMinute int
	MaxRequestsPerHour   int
	WindowSize           time.Duration
	WindowSlots          int

	// Burst detection
	BurstThreshold     int
	BurstWindow        time.Duration
	MaxBurstsPerMinute int

	// Adaptive throttling
	AdaptiveEnabled    bool
	SuspicionThreshold int
	ThrottleMultiplier float64

	// Blocking
	BlockingEnabled bool
	BlockDuration   time.Duration
	BlockThreshold  int

	// Cleanup
	CleanupInterval time.Duration
	ClientTimeout   time.Duration
}

var (
	cfg     *Config
	cfgOnce sync.Once
)

// Get returns the singleton configuration instance
func Get() *Config {
	cfgOnce.Do(func() {
		cfg = MustLoad()
	})
	return cfg
}

// loadConfig loads configuration from environment variables with validation
func loadConfig() *Config {
	c := &Config{
		// DNS Server Configuration
		DNSPort:    util.GetEnvString("DNS_PORT", ":53"),
		UDPSize:    util.GetEnvInt("UDP_SIZE", 65535),
		DNSTimeout: util.GetEnvDuration("DNS_TIMEOUT", 5*time.Second),

		// DNS Servers Configuration
		PrivateServers: util.GetEnvStringSlice("PRIVATE_DNS_SERVERS", ""),
		PublicServers:  util.GetEnvStringSlice("PUBLIC_DNS_SERVERS", DefaultPublicDNS),
		CacheRefresh:   util.GetEnvDuration("CACHE_SERVERS_REFRESH", 10*time.Second),
		WorkerCount:    util.GetEnvInt("WORKER_COUNT", 5),
		TestDomain:     util.GetEnvString("TEST_DOMAIN", "google.com"),
		DNSStatslog:    util.GetEnvDuration("DNS_STATSLOG", 5*time.Minute),

		// Cache Configuration
		CacheSize: util.GetEnvInt("CACHE_SIZE", 10000),
		CacheTTL:  util.GetEnvDuration("DNS_CACHE_TTL", 30*time.Minute),

		// Metrics Configuration
		MetricsPort:       util.GetEnvString("METRICS_PORT", ":8080"),
		EnableMetrics:     util.GetEnvBool("ENABLE_METRICS", true),
		MetricsBatchSize:  util.GetEnvInt("METRICS_BATCH_SIZE", 500),
		MetricsBatchDelay: util.GetEnvDuration("METRICS_BATCH_DELAY", 100*time.Millisecond),

		// Client Routing Configuration
		PublicOnlyClients:    util.GetEnvStringSlice("PUBLIC_ONLY_CLIENTS", ""),
		PublicOnlyClientMACs: util.GetEnvStringSlice("PUBLIC_ONLY_CLIENT_MACS", ""),
		EnableClientRouting:  util.GetEnvBool("ENABLE_CLIENT_ROUTING", false),

		// Domain Routing Configuration
		EnableDomainRouting:              util.GetEnvBool("ENABLE_DOMAIN_ROUTING", false),
		DomainRoutingFolder:              util.GetEnvString("DOMAIN_ROUTING_FOLDER", ""),
		DomainRoutingTableReloadInterval: util.GetEnvInt("DOMAIN_ROUTING_TABLE_RELOAD_INTERVAL", 60),

		// Logging Configuration
		LogLevel: util.GetEnvString("LOG_LEVEL", "info"),

		// Cache Persistence Configuration
		EnableCachePersistence:   util.GetEnvBool("ENABLE_CACHE_PERSISTENCE", true),
		CachePersistenceFile:     util.GetEnvString("CACHE_PERSISTENCE_FILE", "/app/cache/dns_cache.json"),
		CachePersistenceInterval: util.GetEnvDuration("CACHE_PERSISTENCE_INTERVAL", 5*time.Minute),
		CachePersistenceMaxAge:   util.GetEnvDuration("CACHE_PERSISTENCE_MAX_AGE", 1*time.Hour),

		// Rate Limiting Configuration
		EnableRateLimit: util.GetEnvBool("ENABLE_RATE_LIMIT", false),
		// Basic rate limiting - default to generous limits
		MaxRequestsPerSecond: util.GetEnvInt("RATE_LIMIT_QPS", 100),
		MaxRequestsPerMinute: util.GetEnvInt("RATE_LIMIT_QPM", 3000),
		MaxRequestsPerHour:   util.GetEnvInt("RATE_LIMIT_QPH", 50000),
		WindowSize:           util.GetEnvDuration("RATE_LIMIT_WINDOW_SIZE", time.Minute),
		WindowSlots:          util.GetEnvInt("RATE_LIMIT_WINDOW_SLOTS", 60),

		// Burst detection
		BurstThreshold:     util.GetEnvInt("RATE_LIMIT_BURST_THRESHOLD", 200),
		BurstWindow:        util.GetEnvDuration("RATE_LIMIT_BURST_WINDOW", time.Second*5),
		MaxBurstsPerMinute: util.GetEnvInt("RATE_LIMIT_MAX_BURSTS_PER_MIN", 5),

		// Adaptive throttling
		AdaptiveEnabled:    util.GetEnvBool("RATE_LIMIT_ADAPTIVE_ENABLED", true),
		SuspicionThreshold: util.GetEnvInt("RATE_LIMIT_SUSPICION_THRESHOLD", 30),
		ThrottleMultiplier: util.GetEnvFloat64("RATE_LIMIT_THROTTLE_MULTIPLIER", 0.5),

		// Blocking
		BlockingEnabled: util.GetEnvBool("RATE_LIMIT_BLOCKING_ENABLED", true),
		BlockDuration:   util.GetEnvDuration("RATE_LIMIT_BLOCK_DURATION", time.Minute*5),
		BlockThreshold:  util.GetEnvInt("RATE_LIMIT_BLOCK_THRESHOLD", 80),

		// Cleanup
		CleanupInterval: util.GetEnvDuration("RATE_LIMIT_CLEANUP_INTERVAL", time.Minute*5),
		ClientTimeout:   util.GetEnvDuration("RATE_LIMIT_CLIENT_TIMEOUT", time.Minute*30),
	}

	return c
}

// MustLoad loads and validates configuration, panicking on validation errors
func MustLoad() *Config {
	c := loadConfig()
	if err := c.Validate(); err != nil {
		panic(fmt.Sprintf("Invalid configuration: %v", err))
	}
	return c
}

// resetForTesting resets the singleton for testing purposes
func resetForTesting() {
	cfg = nil
	cfgOnce = sync.Once{}
}

// Validate performs configuration validation
func (c *Config) Validate() error {
	if c.WorkerCount <= 0 {
		return fmt.Errorf("WORKER_COUNT must be positive, got %d", c.WorkerCount)
	}
	if c.UDPSize <= 0 || c.UDPSize > 65535 {
		return fmt.Errorf("UDP_SIZE must be between 1 and 65535, got %d", c.UDPSize)
	}
	if c.CacheSize <= 0 {
		return fmt.Errorf("CACHE_SIZE must be positive, got %d", c.CacheSize)
	}
	if c.DNSTimeout <= 0 {
		return fmt.Errorf("DNS_TIMEOUT must be positive, got %v", c.DNSTimeout)
	}
	if c.CacheRefresh <= 0 {
		return fmt.Errorf("CACHE_SERVERS_REFRESH must be positive, got %v", c.CacheRefresh)
	}
	if c.CacheTTL < MinCacheTTL || c.CacheTTL > MaxCacheTTL {
		return fmt.Errorf("DNS_CACHE_TTL must be between %v and %v, got %v", MinCacheTTL, MaxCacheTTL, c.CacheTTL)
	}
	if c.DomainRoutingTableReloadInterval <= 0 {
		return fmt.Errorf("DOMAIN_ROUTING_TABLE_RELOAD_INTERVAL must be positive, got %d", c.DomainRoutingTableReloadInterval)
	}
	return nil
}
