package dnssource

import (
	"dnsloadbalancer/clientrouting"
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// ServerType represents the category of DNS server
type ServerType uint8

const (
	ServerTypeUnknown ServerType = iota
	ServerTypePrivate
	ServerTypePublic
)

var (
	CacheLastUpdated         time.Time
	CacheMutex               sync.RWMutex
	PrivateAndPublicFallback []string                      // Combined list of private and public servers
	serverTypeMap            = make(map[string]ServerType) // Single map for O(1) server type lookup
	PublicServersFallback    []string                      // Copy of public servers for fallback
	PrivateServersCache      []string                      // Cache for private servers which are healthy
	PublicServersCache       []string                      // Cache for public servers which are healthy
	cfg                      = config.Get()
)

type metricsRecorderInterface interface {
	RecordError(string, string)
	SetUpstreamServersTotal(int)
	SetUpstreamServerReachable(string, bool)
	RecordUpstreamQuery(string, string, time.Duration)
}

func InitDNSSource(metricsRecorder metricsRecorderInterface) {
	// Build single server type map - one lookup instead of two
	for _, s := range cfg.PrivateServers {
		if s != "" {
			serverTypeMap[s] = ServerTypePrivate
		}
	}
	for _, s := range cfg.PublicServers {
		if s != "" {
			serverTypeMap[s] = ServerTypePublic
		}
	}

	// Pre-allocate with exact capacity to avoid reallocations
	totalCapacity := len(cfg.PrivateServers) + len(cfg.PublicServers)
	PrivateAndPublicFallback = make([]string, 0, totalCapacity)
	PrivateAndPublicFallback = append(PrivateAndPublicFallback, cfg.PrivateServers...)
	PrivateAndPublicFallback = append(PrivateAndPublicFallback, cfg.PublicServers...)

	// Create a copy with proper capacity
	PublicServersFallback = make([]string, len(cfg.PublicServers))
	copy(PublicServersFallback, cfg.PublicServers)

	logutil.Logger.Infof("Private servers: %v", cfg.PrivateServers)
	logutil.Logger.Infof("Public servers: %v", cfg.PublicServers)
	logutil.Logger.Infof("Combined servers: %v", PrivateAndPublicFallback)
	logutil.Logger.Debug("InitDNSSource: end")
}

// GetDNSServers returns the list of DNS servers from environment or config which can be used for DNS queries.
// It combines private and public servers
func GetDNSServers() []string {
	return PrivateAndPublicFallback
}

// serverHealthCheckResult holds the result of a server health check
type serverHealthCheckResult struct {
	server      string
	isReachable bool
	rtt         time.Duration
	err         error
}

// performHealthCheck performs a single health check on a DNS server
func performHealthCheck(server, testDomain string, dnsClient *dns.Client, dnsMsgPool *sync.Pool) serverHealthCheckResult {
	m := dnsMsgPool.Get().(*dns.Msg)
	defer dnsMsgPool.Put(m) // Ensure cleanup even on panic

	m.SetQuestion(testDomain, dns.TypeA)
	m.RecursionDesired = true
	_, rtt, err := dnsClient.Exchange(m, server)
	return serverHealthCheckResult{
		server:      server,
		isReachable: err == nil,
		rtt:         rtt,
		err:         err,
	}
}
func UpdateDNSServersCache(metricsRecorder metricsRecorderInterface,
	cacheTTL time.Duration,
	clientRoutingEnabled bool,
	privateServers, publicServers []string,
	dnsClient *dns.Client,
	dnsMsgPool *sync.Pool) {
	servers := GetDNSServers() // Get all configured DNS servers
	if len(servers) == 0 || (len(servers) == 1 && servers[0] == "") {
		metricsRecorder.RecordError("no_dns_servers", "config")
		logutil.Logger.Fatalf("No DNS servers found")
	}

	upstreamServerCount := len(servers)
	enableMetrics := cfg.EnableMetrics
	if enableMetrics {
		metricsRecorder.SetUpstreamServersTotal(upstreamServerCount)
	}

	// Create unique servers slice directly, filtering empty strings
	uniqueServers := make([]string, 0, len(servers))
	serverSet := make(map[string]struct{}, len(servers))

	for _, server := range servers {
		if server != "" {
			if _, exists := serverSet[server]; !exists {
				serverSet[server] = struct{}{}
				uniqueServers = append(uniqueServers, server)
			}
		}
	}

	var (
		reachablePrivate = make([]string, 0, len(privateServers)) // preallocate
		reachablePublic  = make([]string, 0, len(publicServers))  // preallocate
		mu               sync.Mutex
		wg               sync.WaitGroup
		sem              = make(chan struct{}, cfg.WorkerCount)
	)

	// Pre-build test domain string to avoid repeated concatenation
	testDomain := cfg.TestDomain + "."

	for _, server := range uniqueServers {
		wg.Add(1)
		sem <- struct{}{}
		go func(svr string) {
			defer wg.Done()
			defer func() { <-sem }()

			result := performHealthCheck(svr, testDomain, dnsClient, dnsMsgPool)

			if !result.isReachable {
				logutil.Logger.Warnf("server %s is not reachable: %v", svr, result.err)
				if enableMetrics {
					metricsRecorder.SetUpstreamServerReachable(svr, false)
					metricsRecorder.RecordUpstreamQuery(svr, "error", result.rtt)
					metricsRecorder.RecordError("upstream_unreachable", "health_check")
				}
				return
			}

			if enableMetrics {
				metricsRecorder.SetUpstreamServerReachable(svr, true)
				metricsRecorder.RecordUpstreamQuery(svr, "success", result.rtt)
			}

			// Minimize lock contention by determining server type before acquiring lock
			isPrivate, isPublic := categorizeServer(svr)

			mu.Lock()
			if isPrivate {
				reachablePrivate = append(reachablePrivate, svr)
			} else if isPublic {
				reachablePublic = append(reachablePublic, svr)
			}
			mu.Unlock()
		}(server)
	}

	wg.Wait()
	if len(reachablePrivate)+len(reachablePublic) == 0 {
		if enableMetrics {
			metricsRecorder.RecordError("no_reachable_servers", "health_check")
		}
		logutil.Logger.Warn("No reachable DNS servers found")
	}
	CacheMutex.Lock()
	PrivateServersCache = reachablePrivate
	PublicServersCache = reachablePublic
	CacheLastUpdated = time.Now()
	CacheMutex.Unlock()
	logutil.Logger.Debug("UpdateDNSServersCache: end")
}

func GetServersForClient(clientIP string, cacheMutex *sync.RWMutex) (privateServers []string, publicServers []string) {
	if clientrouting.ShouldUsePublicServers(clientIP) {
		cacheMutex.RLock()
		defer cacheMutex.RUnlock()
		if len(PublicServersCache) > 0 {
			return []string{}, copyServers(PublicServersCache)
		}
		return []string{}, PublicServersFallback
	}

	cacheMutex.RLock()
	defer cacheMutex.RUnlock()

	if len(PrivateServersCache) == 0 && len(PublicServersCache) == 0 {
		return []string{}, PrivateAndPublicFallback
	}

	return copyServers(PrivateServersCache), copyServers(PublicServersCache)
}

// copyServers creates a copy of the server slice to avoid returning references to internal slices
func copyServers(servers []string) []string {
	if len(servers) == 0 {
		return []string{}
	}
	result := make([]string, len(servers))
	copy(result, servers)
	return result
}

// categorizeServer returns (isPrivate, isPublic) using single map lookup
// Optimized: O(1) with single map access instead of two separate map lookups
func categorizeServer(server string) (isPrivate, isPublic bool) {
	serverType := serverTypeMap[server]
	return serverType == ServerTypePrivate, serverType == ServerTypePublic
}
