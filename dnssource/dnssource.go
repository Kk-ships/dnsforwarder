package dnssource

import (
	"crypto/tls"
	"dnsloadbalancer/clientrouting"
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

// ServerType represents the category of DNS server
type ServerType uint8

const (
	ServerTypeUnknown ServerType = iota
	ServerTypePrivate
	ServerTypePublic
	ServerTypeDoT
)

// serverCache holds server lists for atomic swap
type serverCache struct {
	privateServers []string
	publicServers  []string
	doTServers     []string
}

var (
	CacheLastUpdated         time.Time
	PrivateAndPublicFallback []string                      // Combined list of private and public servers
	serverTypeMap            = make(map[string]ServerType) // Single map for O(1) server type lookup
	PublicServersFallback    []string                      // Copy of public servers for fallback

	// Use atomic.Value for lock-free reads of server caches
	atomicServerCache atomic.Value // stores *serverCache

	cfg = config.Get()
)

type metricsRecorderInterface interface {
	RecordError(string, string)
	SetUpstreamServersTotal(int)
	SetUpstreamServerReachable(string, bool)
	RecordUpstreamQuery(string, string, time.Duration)
}

func InitDNSSource(metricsRecorder metricsRecorderInterface) {
	// Clear the server type map to ensure clean state
	serverTypeMap = make(map[string]ServerType)

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
	// Add DoT servers to the map
	if cfg.EnableDoT {
		for _, s := range cfg.DoTServers {
			if s != "" {
				serverTypeMap[s] = ServerTypeDoT
			}
		}
	}

	// Pre-allocate with exact capacity to avoid reallocations
	totalCapacity := len(cfg.PrivateServers) + len(cfg.PublicServers)
	if cfg.EnableDoT {
		totalCapacity += len(cfg.DoTServers)
	}
	PrivateAndPublicFallback = make([]string, 0, totalCapacity)
	PrivateAndPublicFallback = append(PrivateAndPublicFallback, cfg.PrivateServers...)
	PrivateAndPublicFallback = append(PrivateAndPublicFallback, cfg.PublicServers...)
	if cfg.EnableDoT {
		PrivateAndPublicFallback = append(PrivateAndPublicFallback, cfg.DoTServers...)
	}

	// Create a copy with proper capacity
	PublicServersFallback = make([]string, len(cfg.PublicServers))
	copy(PublicServersFallback, cfg.PublicServers)

	// Initialize atomic server cache with empty lists
	atomicServerCache.Store(&serverCache{
		privateServers: []string{},
		publicServers:  []string{},
		doTServers:     []string{},
	})

	logutil.Logger.Infof("Private servers: %v", cfg.PrivateServers)
	logutil.Logger.Infof("Public servers: %v", cfg.PublicServers)
	if cfg.EnableDoT {
		logutil.Logger.Infof("DoT servers: %v", cfg.DoTServers)
	}
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

	// Check if this is a DoT server and use appropriate client
	var client *dns.Client
	if IsDoTServer(server) {
		// Create DoT client for this health check
		client = &dns.Client{
			Timeout: cfg.DNSTimeout,
			Net:     "tcp-tls",
			TLSConfig: &tls.Config{
				ServerName:         cfg.DoTServerName,
				InsecureSkipVerify: cfg.DoTSkipVerify,
			},
		}
	} else {
		client = dnsClient
	}

	_, rtt, err := client.Exchange(m, server)
	return serverHealthCheckResult{
		server:      server,
		isReachable: err == nil,
		rtt:         rtt,
		err:         err,
	}
}

// IsDoTServer checks if a server is configured as a DoT server
func IsDoTServer(server string) bool {
	if !cfg.EnableDoT {
		return false
	}
	return serverTypeMap[server] == ServerTypeDoT
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
		logutil.Logger.Errorf("No DNS servers found - continuing with empty cache")
		// Don't crash the application, just log the error and continue
		// This allows the application to stay running and potentially recover
		return
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
		reachableDoT     = make([]string, 0, len(cfg.DoTServers)) // preallocate
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
			serverType := serverTypeMap[svr]

			mu.Lock()
			switch serverType {
			case ServerTypePrivate:
				reachablePrivate = append(reachablePrivate, svr)
			case ServerTypePublic:
				reachablePublic = append(reachablePublic, svr)
			case ServerTypeDoT:
				reachableDoT = append(reachableDoT, svr)
			}
			mu.Unlock()
		}(server)
	}

	wg.Wait()
	totalReachable := len(reachablePrivate) + len(reachablePublic) + len(reachableDoT)
	if totalReachable == 0 {
		if enableMetrics {
			metricsRecorder.RecordError("no_reachable_servers", "health_check")
		}
		logutil.Logger.Warn("No reachable DNS servers found")
	}

	// Atomically swap the server cache - lock-free reads!
	atomicServerCache.Store(&serverCache{
		privateServers: reachablePrivate,
		publicServers:  reachablePublic,
		doTServers:     reachableDoT,
	})

	CacheLastUpdated = time.Now()
	logutil.Logger.Debug("UpdateDNSServersCache: end")
}

func GetServersForClient(clientIP string) (privateServers []string, publicServers []string) {
	// Lock-free read of server cache using atomic.Value
	cacheVal := atomicServerCache.Load()
	if cacheVal == nil {
		// Cache not initialized yet, fall back to static configs
		return []string{}, PrivateAndPublicFallback
	}

	cache := cacheVal.(*serverCache)

	if clientrouting.ShouldUsePublicServers(clientIP) {
		// Combine public servers and DoT servers for public-only clients
		combinedPublic := make([]string, 0, len(cache.publicServers)+len(cache.doTServers))
		combinedPublic = append(combinedPublic, cache.publicServers...)
		combinedPublic = append(combinedPublic, cache.doTServers...)

		if len(combinedPublic) > 0 {
			return []string{}, combinedPublic
		}
		return []string{}, PublicServersFallback
	}

	if len(cache.privateServers) == 0 && len(cache.publicServers) == 0 && len(cache.doTServers) == 0 {
		return []string{}, PrivateAndPublicFallback
	}

	// Combine public and DoT servers as fallback
	combinedPublic := make([]string, 0, len(cache.publicServers)+len(cache.doTServers))
	combinedPublic = append(combinedPublic, cache.publicServers...)
	combinedPublic = append(combinedPublic, cache.doTServers...)

	// Return private servers first, then combined public+DoT as fallback
	return cache.privateServers, combinedPublic
}

// SetConfigForTest sets a test configuration - for testing only
func SetConfigForTest(testCfg *config.Config) {
	cfg = testCfg
}
