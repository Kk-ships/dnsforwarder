package config

import (
	"dnsloadbalancer/util"
	"time"
)

var (
	DefaultCacheTTL    = util.GetEnvDuration("CACHE_TTL", 10*time.Second)
	DefaultDNSTimeout  = util.GetEnvDuration("DNS_TIMEOUT", 5*time.Second)
	DefaultWorkerCount = util.GetEnvInt("WORKER_COUNT", 5)
	DefaultTestDomain  = util.GetEnvString("TEST_DOMAIN", "google.com")
	DefaultDNSPort     = util.GetEnvString("DNS_PORT", ":53")
	DefaultUDPSize     = util.GetEnvInt("UDP_SIZE", 65535)
	DefaultDNSStatslog = util.GetEnvDuration("DNS_STATSLOG", 5*time.Minute)
	DefaultDNSServer   = util.GetEnvString("DEFAULT_DNS_SERVER", "8.8.8.8:53")
	DefaultCacheSize   = util.GetEnvInt("CACHE_SIZE", 10000)
	DefaultDNSCacheTTL = util.GetEnvDuration("DNS_CACHE_TTL", 30*time.Minute)
	DefaultMetricsPort = util.GetEnvString("METRICS_PORT", ":8080")
	EnableMetrics      = util.GetEnvBool("ENABLE_METRICS", true)

	// Client-based routing configuration
	PrivateServers       = util.GetEnvStringSlice("PRIVATE_DNS_SERVERS", "192.168.1.1:53")
	PublicServers        = util.GetEnvStringSlice("PUBLIC_DNS_SERVERS", "1.1.1.1:53,8.8.8.8:53")
	PublicOnlyClients    = util.GetEnvStringSlice("PUBLIC_ONLY_CLIENTS", "")
	PublicOnlyClientMACs = util.GetEnvStringSlice("PUBLIC_ONLY_CLIENT_MACS", "")
	EnableClientRouting  = util.GetEnvBool("ENABLE_CLIENT_ROUTING", false)
)
