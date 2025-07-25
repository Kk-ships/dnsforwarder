package config

import (
	"dnsloadbalancer/util"
	"time"
)

const ARecordInvalidAnswer = "0.0.0.0"
const AAAARecordInvalidAnswer = "::"
const NegativeResponseTTLDivisor = 4
const MinCacheTTL = 30 * time.Second
const MaxCacheTTL = 24 * time.Hour

const DefaultPublicDNS = "1.1.1.1:53"

var (
	PrivateServers      = util.GetEnvStringSlice("PRIVATE_DNS_SERVERS", "")
	PublicServers       = util.GetEnvStringSlice("PUBLIC_DNS_SERVERS", DefaultPublicDNS)
	DefaultCacheRefresh = util.GetEnvDuration("CACHE_SERVERS_REFRESH", 10*time.Second) // Default Duration for DNS servers Health Check
	DefaultDNSTimeout   = util.GetEnvDuration("DNS_TIMEOUT", 5*time.Second)
	DefaultWorkerCount  = util.GetEnvInt("WORKER_COUNT", 5)
	DefaultTestDomain   = util.GetEnvString("TEST_DOMAIN", "google.com")
	DefaultDNSPort      = util.GetEnvString("DNS_PORT", ":53")
	DefaultUDPSize      = util.GetEnvInt("UDP_SIZE", 65535)
	DefaultDNSStatslog  = util.GetEnvDuration("DNS_STATSLOG", 5*time.Minute)
	DefaultCacheSize    = util.GetEnvInt("CACHE_SIZE", 10000)
	DefaultDNSCacheTTL  = util.GetEnvDuration("DNS_CACHE_TTL", 30*time.Minute)

	// Metrics configuration
	DefaultMetricsPort = util.GetEnvString("METRICS_PORT", ":8080")
	EnableMetrics      = util.GetEnvBool("ENABLE_METRICS", true)

	// Client-based routing configuration
	PublicOnlyClients    = util.GetEnvStringSlice("PUBLIC_ONLY_CLIENTS", "")
	PublicOnlyClientMACs = util.GetEnvStringSlice("PUBLIC_ONLY_CLIENT_MACS", "")
	EnableClientRouting  = util.GetEnvBool("ENABLE_CLIENT_ROUTING", false)

	// Domain routing configuration
	EnableDomainRouting              = util.GetEnvBool("ENABLE_DOMAIN_ROUTING", false)
	DomainRoutingFolder              = util.GetEnvString("DOMAIN_ROUTING_FOLDER", "")
	DomainRoutingTableReloadInterval = util.GetEnvInt("DOMAIN_ROUTING_TABLE_RELOAD_INTERVAL", 60) // Interval to reload domain routing table in seconds

	// Logging configuration
	LogLevel = util.GetEnvString("LOG_LEVEL", "info")

	// Cache persistence configuration
	EnableCachePersistence   = util.GetEnvBool("ENABLE_CACHE_PERSISTENCE", true)
	CachePersistenceFile     = util.GetEnvString("CACHE_PERSISTENCE_FILE", "/app/cache/dns_cache.json")
	CachePersistenceInterval = util.GetEnvDuration("CACHE_PERSISTENCE_INTERVAL", 5*time.Minute)
	CachePersistenceMaxAge   = util.GetEnvDuration("CACHE_PERSISTENCE_MAX_AGE", 1*time.Hour)

	// Multiple interface and routing configuration
	ListenInterfaces     = util.GetEnvStringSlice("LISTEN_INTERFACES", "")   // Comma-separated list of interfaces to listen on (e.g., "eth0,eth1")
	OutboundInterface    = util.GetEnvString("OUTBOUND_INTERFACE", "")       // Outbound interface for upstream queries
	BindToInterface      = util.GetEnvBool("BIND_TO_INTERFACE", false)       // Whether to bind to specific interfaces
	DefaultListenAddress = util.GetEnvString("LISTEN_ADDRESS", "0.0.0.0:53") // Default listen address if no interfaces specified

	// PID file configuration
	EnablePIDFile = util.GetEnvBool("ENABLE_PID_FILE", false)
	PIDFilePath   = util.GetEnvString("PID_FILE_PATH", "/var/run/dnsforwarder.pid")

	// EDNS Client Subnet configuration
	EnableEDNSClientSubnet = util.GetEnvBool("ENABLE_EDNS_CLIENT_SUBNET", false)
	EDNSClientSubnetScope  = util.GetEnvInt("EDNS_CLIENT_SUBNET_SCOPE", 24) // Default subnet scope for IPv4
	ForwardClientIP        = util.GetEnvBool("FORWARD_CLIENT_IP", true)     // Whether to forward real client IP in EDNS
)
