package main

import (
	"context"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/util"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

// --- Reusable Helpers with Reduced Lock Contention ---

// --- Config ---
var (
	defaultCacheTTL    = util.GetEnvDuration("CACHE_TTL", 10*time.Second)
	defaultDNSTimeout  = util.GetEnvDuration("DNS_TIMEOUT", 5*time.Second)
	defaultWorkerCount = util.GetEnvInt("WORKER_COUNT", 5)
	defaultTestDomain  = util.GetEnvString("TEST_DOMAIN", "google.com")
	defaultDNSPort     = util.GetEnvString("DNS_PORT", ":53")
	defaultUDPSize     = util.GetEnvInt("UDP_SIZE", 65535)
	defaultDNSStatslog = util.GetEnvDuration("DNS_STATSLOG", 5*time.Minute)
	defaultDNSServer   = util.GetEnvString("DEFAULT_DNS_SERVER", "8.8.8.8:53")
	defaultCacheSize   = util.GetEnvInt("CACHE_SIZE", 10000)
	defaultDNSCacheTTL = util.GetEnvDuration("DNS_CACHE_TTL", 30*time.Minute)
	defaultMetricsPort = util.GetEnvString("METRICS_PORT", ":8080")
	enableMetrics      = util.GetEnvBool("ENABLE_METRICS", true)

	// Client-based routing configuration
	privateServers      = util.GetEnvStringSlice("PRIVATE_DNS_SERVERS", "192.168.1.1:53")       // Private DNS servers (PiHole, AdGuard, etc.)
	publicServers       = util.GetEnvStringSlice("PUBLIC_DNS_SERVERS", "1.1.1.1:53,8.8.8.8:53") // Public DNS servers (fallback)
	publicOnlyClients   = util.GetEnvStringSlice("PUBLIC_ONLY_CLIENTS", "")                     // Clients that should use public servers only
	enableClientRouting = util.GetEnvBool("ENABLE_CLIENT_ROUTING", false)                       // Enable client-based routing
)

var (
	reachableServersCache []string
	cacheLastUpdated      time.Time
	cacheMutex            sync.RWMutex
	cacheTTL              = defaultCacheTTL
	dnsClient             = &dns.Client{Timeout: defaultDNSTimeout}

	// Client routing cache
	privateServersCache      []string
	publicServersCache       []string
	publicOnlyClientsMap     sync.Map // map[string]bool for fast lookup
	privateServersSet        map[string]struct{}
	publicServersSet         map[string]struct{}
	privateAndPublicFallback []string
	publicServersFallback    []string
)

var dnsMsgPool = sync.Pool{
	New: func() interface{} {
		return new(dns.Msg)
	},
}
var (
	dnsUsageStats = make(map[string]int)
	statsMutex    sync.Mutex
)

// --- Client Routing Logic ---

func initializeClientRouting() {
	if !enableClientRouting {
		return
	}

	// Precompute sets and fallback slices
	privateServersSet = make(map[string]struct{})
	publicServersSet = make(map[string]struct{})
	for _, s := range privateServers {
		if s != "" {
			privateServersSet[s] = struct{}{}
		}
	}
	for _, s := range publicServers {
		if s != "" {
			publicServersSet[s] = struct{}{}
		}
	}
	privateAndPublicFallback = append([]string{}, privateServers...)
	privateAndPublicFallback = append(privateAndPublicFallback, publicServers...)
	publicServersFallback = append([]string{}, publicServers...)

	for _, client := range publicOnlyClients {
		if client != "" {
			publicOnlyClientsMap.Store(strings.TrimSpace(client), true)
			logutil.LogWithBufferf("[CLIENT-ROUTING] Configured client %s to use public servers only", client)
		}
	}

	logutil.LogWithBufferf("[CLIENT-ROUTING] Private servers: %v", privateServers)
	logutil.LogWithBufferf("[CLIENT-ROUTING] Public servers: %v", publicServers)
	logutil.LogWithBufferf("[CLIENT-ROUTING] Client routing enabled: %v", enableClientRouting)
}

func getClientIP(w dns.ResponseWriter) string {
	// Parse once, handle both UDP and TCP
	addr := w.RemoteAddr()
	switch a := addr.(type) {
	case *net.UDPAddr:
		return a.IP.String()
	case *net.TCPAddr:
		return a.IP.String()
	default:
		// fallback: parse string
		s := addr.String()
		if i := strings.LastIndex(s, ":"); i > 0 {
			return s[:i]
		}
		return s
	}
}

func shouldUsePublicServers(clientIP string) bool {
	if !enableClientRouting {
		return false
	}

	// Check if client is in public-only list
	if _, exists := publicOnlyClientsMap.Load(clientIP); exists {
		return true
	}

	return false
}

func getServersForClient(clientIP string) []string {
	// Avoid unnecessary copies, precompute fallback, and clarify lock usage
	if !enableClientRouting {
		return getCachedDNSServers()
	}

	// Use precomputed maps for lookup
	if shouldUsePublicServers(clientIP) {
		cacheMutex.RLock()
		servers := publicServersCache
		cacheMutex.RUnlock()
		if len(servers) > 0 {
			return servers
		}
		return publicServersFallback
	}

	cacheMutex.RLock()
	priv := privateServersCache
	pub := publicServersCache
	cacheMutex.RUnlock()
	if len(priv) == 0 && len(pub) == 0 {
		return privateAndPublicFallback
	}
	if len(pub) == 0 {
		return priv
	}
	if len(priv) == 0 {
		return pub
	}
	servers := make([]string, 0, len(priv)+len(pub))
	servers = append(servers, priv...)
	servers = append(servers, pub...)
	return servers
}

// --- DNS Server Selection ---

func getDNSServers() []string {
	return util.GetEnvStringSlice("DNS_SERVERS", defaultDNSServer)
}

func updateDNSServersCache() {
	servers := getDNSServers()
	if len(servers) == 0 || (len(servers) == 1 && servers[0] == "") {
		metricsRecorder.RecordError("no_dns_servers", "config")
		logutil.LogWithBufferFatalf("[ERROR] no DNS servers found")
	}

	// Update total servers metric
	if enableMetrics {
		metricsRecorder.SetUpstreamServersTotal(len(servers))
	}

	type result struct {
		server string
		ok     bool
	}

	// Test all servers (legacy + private + public)
	allServersToTest := make([]string, 0)
	allServersToTest = append(allServersToTest, servers...)

	if enableClientRouting {
		allServersToTest = append(allServersToTest, privateServers...)
		allServersToTest = append(allServersToTest, publicServers...)
	}

	// Remove duplicates
	serverSet := make(map[string]bool)
	uniqueServers := make([]string, 0)
	for _, server := range allServersToTest {
		if server != "" && !serverSet[server] {
			serverSet[server] = true
			uniqueServers = append(uniqueServers, server)
		}
	}

	m := dnsMsgPool.Get().(*dns.Msg)
	defer dnsMsgPool.Put(m)
	m.SetQuestion(dns.Fqdn(defaultTestDomain), dns.TypeA)
	m.RecursionDesired = true

	reachableCh := make(chan result, len(uniqueServers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, defaultWorkerCount)

	for _, server := range uniqueServers {
		wg.Add(1)
		sem <- struct{}{}
		go func(svr string) {
			defer wg.Done()
			defer func() { <-sem }()

			start := time.Now()
			_, _, err := dnsClient.Exchange(m.Copy(), svr)
			duration := time.Since(start)

			if err != nil {
				logutil.LogWithBufferf("[WARNING] server %s is not reachable: %v", svr, err)
				if enableMetrics {
					metricsRecorder.SetUpstreamServerReachable(svr, false)
					metricsRecorder.RecordUpstreamQuery(svr, "error", duration)
					metricsRecorder.RecordError("upstream_unreachable", "health_check")
				}
				reachableCh <- result{server: svr, ok: false}
				return
			}

			if enableMetrics {
				metricsRecorder.SetUpstreamServerReachable(svr, true)
				metricsRecorder.RecordUpstreamQuery(svr, "success", duration)
			}
			reachableCh <- result{server: svr, ok: true}
		}(server)
	}

	go func() {
		wg.Wait()
		close(reachableCh)
	}()

	var reachable []string
	var reachablePrivate []string
	var reachablePublic []string

	for res := range reachableCh {
		if res.ok {
			reachable = append(reachable, res.server)
			if enableClientRouting {
				// Use precomputed sets for O(1) lookup
				if _, ok := privateServersSet[res.server]; ok {
					reachablePrivate = append(reachablePrivate, res.server)
				} else if _, ok := publicServersSet[res.server]; ok {
					reachablePublic = append(reachablePublic, res.server)
				}
			}
		}
	}

	if len(reachable) == 0 {
		metricsRecorder.RecordError("no_reachable_servers", "health_check")
		logutil.LogWithBufferFatalf("[ERROR] no reachable DNS servers found")
	}

	cacheMutex.Lock()
	reachableServersCache = reachable
	if enableClientRouting {
		privateServersCache = reachablePrivate
		publicServersCache = reachablePublic
	}
	cacheLastUpdated = time.Now()
	cacheMutex.Unlock()
}

func getCachedDNSServers() []string {
	cacheMutex.RLock()
	servers := make([]string, len(reachableServersCache))
	copy(servers, reachableServersCache)
	cacheValid := time.Since(cacheLastUpdated) <= cacheTTL && len(servers) > 0
	cacheMutex.RUnlock()

	if cacheValid {
		return servers
	}

	// Update cache and try again
	updateDNSServersCache()

	cacheMutex.RLock()
	servers = make([]string, len(reachableServersCache))
	copy(servers, reachableServersCache)
	cacheMutex.RUnlock()
	return servers
}

// --- DNS Usage Logger ---

func startDNSUsageLogger() {
	ticker := time.NewTicker(defaultDNSStatslog)
	defer ticker.Stop()
	for range ticker.C {
		statsMutex.Lock()
		logutil.LogWithBufferf("DNS Usage Stats: %+v", dnsUsageStats)
		statsMutex.Unlock()
	}
}

func startDNSServerCacheUpdater() {
	go startDNSUsageLogger()
	// Start cache updater in background
	go func() {
		ticker := time.NewTicker(cacheTTL)
		defer ticker.Stop()
		for range ticker.C {
			updateDNSServersCache()
		}
	}()
}

// --- DNS Resolver ---

func resolver(domain string, qtype uint16) []dns.RR {
	return resolverForClient(domain, qtype, "")
}

func resolverForClient(domain string, qtype uint16, clientIP string) []dns.RR {
	m := dnsMsgPool.Get().(*dns.Msg)
	defer dnsMsgPool.Put(m)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true

	var servers []string
	if clientIP != "" {
		servers = getServersForClient(clientIP)
	} else {
		servers = getCachedDNSServers()
	}

	var response *dns.Msg
	var err error

	for _, svr := range servers {
		start := time.Now()
		response, _, err = dnsClient.Exchange(m, svr)
		duration := time.Since(start)

		if err == nil && response != nil {
			statsMutex.Lock()
			dnsUsageStats[svr]++
			statsMutex.Unlock()

			if enableMetrics {
				metricsRecorder.RecordUpstreamQuery(svr, "success", duration)
			}
			return response.Answer
		}

		logutil.LogWithBufferf("[WARNING] exchange error using server %s: %v", svr, err)
		if enableMetrics {
			metricsRecorder.RecordUpstreamQuery(svr, "error", duration)
			metricsRecorder.RecordError("upstream_query_failed", "dns_exchange")
		}
	}

	if enableMetrics {
		metricsRecorder.RecordError("all_upstream_failed", "dns_resolution")
	}
	logutil.LogWithBufferFatalf("[ERROR] all DNS exchanges failed")
	return nil
}

// --- DNS Handler ---

type dnsHandler struct{}

// Update the ServeDNS method to use the caching resolver
func (h *dnsHandler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true

	if len(r.Question) > 0 {
		q := r.Question[0]
		clientIP := getClientIP(w)

		var answers []dns.RR
		if enableClientRouting && clientIP != "" {
			answers = resolverWithCacheForClient(q.Name, q.Qtype, clientIP)
		} else {
			answers = resolverWithCache(q.Name, q.Qtype)
		}

		if answers != nil {
			msg.Answer = answers
		}
	}

	if err := w.WriteMsg(msg); err != nil {
		if enableMetrics {
			metricsRecorder.RecordError("dns_response_write_failed", "dns_handler")
		}
		logutil.LogWithBufferf("[ERROR] Failed to write DNS response: %v", err)
	}
}

// --- Server Startup ---

func StartDNSServer() {
	// Initialize client routing configuration
	initializeClientRouting()

	updateDNSServersCache()

	handler := new(dnsHandler)
	server := &dns.Server{
		Addr:      defaultDNSPort,
		Net:       "udp",
		Handler:   handler,
		UDPSize:   defaultUDPSize,
		ReusePort: true,
	}

	logutil.LogWithBufferf("Starting DNS server on port 53")
	if enableClientRouting {
		logutil.LogWithBufferf("Client-based DNS routing enabled")
		logutil.LogWithBufferf("Private servers: %v", privateServers)
		logutil.LogWithBufferf("Public servers: %v", publicServers)
		logutil.LogWithBufferf("Public-only clients: %v", publicOnlyClients)
	}

	startDNSServerCacheUpdater()
	startCacheStatsLogger()

	// Start Prometheus metrics server if enabled
	if enableMetrics {
		StartMetricsServer()
		StartMetricsUpdater()
		logutil.LogWithBufferf("Prometheus metrics enabled on %s/metrics", defaultMetricsPort)
	}

	// Channel to listen for errors from ListenAndServe
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	// Set up signal handling for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logutil.LogWithBufferf("Received signal %s, shutting down DNS server gracefully...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.ShutdownContext(ctx); err != nil {
			logutil.LogWithBufferf("[ERROR] Graceful shutdown failed: %v", err)
		} else {
			logutil.LogWithBufferf("DNS server shut down gracefully")
		}
	case err := <-errCh:
		if err != nil {
			logutil.LogWithBufferFatalf("Failed to start server: %s\n", err.Error())
		}
	}
}

func main() {
	StartDNSServer()
}
