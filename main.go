package main

import (
	"context"
	"dnsloadbalancer/cache"
	"dnsloadbalancer/clientrouting"
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/util"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

var dnsMsgPool = sync.Pool{
	New: func() interface{} {
		return new(dns.Msg)
	},
}
var (
	dnsUsageStats = make(map[string]int)
	statsMutex    sync.Mutex

	reachableServersCache []string
	cacheLastUpdated      time.Time
	cacheMutex            sync.RWMutex
	cacheTTL              = config.DefaultCacheTTL
	dnsClient             = &dns.Client{Timeout: config.DefaultDNSTimeout}
)

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
	// Prefer client-specific servers if clientIP is available
	// This allows client routing to take precedence over the general server cache.
	// If client routing is not enabled or no specific servers are found, fallback to cached servers
	// or default servers.
	if config.EnableClientRouting && clientIP != "" {
		servers = clientrouting.GetServersForClient(clientIP, &cacheMutex)
		if servers == nil {
			servers = getCachedDNSServers()
		}
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

			if config.EnableMetrics {
				metricsRecorder.RecordUpstreamQuery(svr, "success", duration)
			}
			return response.Answer
		}

		logutil.LogWithBufferf("[WARNING] exchange error using server %s: %v", svr, err)
		if config.EnableMetrics {
			metricsRecorder.RecordUpstreamQuery(svr, "error", duration)
			metricsRecorder.RecordError("upstream_query_failed", "dns_exchange")
		}
	}

	if config.EnableMetrics {
		metricsRecorder.RecordError("all_upstream_failed", "dns_resolution")
	}
	logutil.LogWithBufferFatalf("[ERROR] all DNS exchanges failed")
	return nil
}

// --- DNS Server Selection ---

func getDNSServers() []string {
	return util.GetEnvStringSlice("DNS_SERVERS", config.DefaultDNSServer)
}

func updateDNSServersCache() {
	servers := getDNSServers()
	if len(servers) == 0 || (len(servers) == 1 && servers[0] == "") {
		metricsRecorder.RecordError("no_dns_servers", "config")
		logutil.LogWithBufferFatalf("[ERROR] no DNS servers found")
	}

	// Update total servers metric
	if config.EnableMetrics {
		metricsRecorder.SetUpstreamServersTotal(len(servers))
	}

	type result struct {
		server string
		ok     bool
	}

	// Test all servers (legacy + private + public)
	allServersToTest := make([]string, 0)
	allServersToTest = append(allServersToTest, servers...)
	if config.EnableClientRouting {
		allServersToTest = append(allServersToTest, config.PrivateServers...)
		allServersToTest = append(allServersToTest, config.PublicServers...)
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
	m.SetQuestion(dns.Fqdn(config.DefaultTestDomain), dns.TypeA)
	m.RecursionDesired = true

	reachableCh := make(chan result, len(uniqueServers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, config.DefaultWorkerCount)

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
				if config.EnableMetrics {
					metricsRecorder.SetUpstreamServerReachable(svr, false)
					metricsRecorder.RecordUpstreamQuery(svr, "error", duration)
					metricsRecorder.RecordError("upstream_unreachable", "health_check")
				}
				reachableCh <- result{server: svr, ok: false}
				return
			}

			if config.EnableMetrics {
				metricsRecorder.SetUpstreamServerReachable(svr, true)
				metricsRecorder.RecordUpstreamQuery(svr, "success", duration)
			}
			reachableCh <- result{server: svr, ok: true}
		}(server)
	}

	wg.Wait()
	close(reachableCh)
	var reachable []string
	var reachablePrivate []string
	var reachablePublic []string

	for res := range reachableCh {
		if res.ok {
			reachable = append(reachable, res.server)
			if config.EnableClientRouting {
				if clientrouting.IsPrivateServer(res.server) {
					reachablePrivate = append(reachablePrivate, res.server)
				} else if clientrouting.IsPublicServer(res.server) {
					reachablePublic = append(reachablePublic, res.server)
				}
			}
		}
	}

	if len(reachable) == 0 {
		if config.EnableMetrics {
			metricsRecorder.RecordError("no_reachable_servers", "health_check")
		}
		logutil.LogWithBufferFatalf("[ERROR] no reachable DNS servers found")
	}

	cacheMutex.Lock()
	if config.EnableClientRouting {
		clientrouting.PrivateServersCache = reachablePrivate
		clientrouting.PublicServersCache = reachablePublic
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
	ticker := time.NewTicker(config.DefaultDNSStatslog)
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
// Now provided by the cache package via dependency injection.

type dnsHandler struct{}

func (h *dnsHandler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true

	if len(r.Question) > 0 {
		q := r.Question[0]
		clientIP := util.GetClientIP(w)
		answers := cache.ResolverWithCache(
			q.Name, q.Qtype,
			resolver, resolverForClient, clientIP,
		)
		if answers != nil {
			msg.Answer = answers
		}
	}

	if err := w.WriteMsg(msg); err != nil {
		if config.EnableMetrics {
			metricsRecorder.RecordError("dns_response_write_failed", "dns_handler")
		}
		logutil.LogWithBufferf("[ERROR] Failed to write DNS response: %v", err)
	}
}

// --- Server Startup ---
func StartDNSServer() {
	clientrouting.InitializeClientRouting()
	updateDNSServersCache()

	// Initialize cache package
	cache.Init(config.DefaultDNSCacheTTL, config.EnableMetrics, metricsRecorder, config.EnableClientRouting)
	cache.StartCacheStatsLogger()

	handler := new(dnsHandler)
	server := &dns.Server{
		Addr:      config.DefaultDNSPort,
		Net:       "udp",
		Handler:   handler,
		UDPSize:   config.DefaultUDPSize,
		ReusePort: true,
	}

	logutil.LogWithBufferf("Starting DNS server on port 53")
	if config.EnableClientRouting {
		logutil.LogWithBufferf("Client-based DNS routing enabled")
		logutil.LogWithBufferf("Private servers: %v", config.PrivateServers)
		logutil.LogWithBufferf("Public servers: %v", config.PublicServers)
		logutil.LogWithBufferf("Public-only clients: %v", config.PublicOnlyClients)
	}

	startDNSServerCacheUpdater()

	// Start Prometheus metrics server if enabled
	if config.EnableMetrics {
		StartMetricsServer()
		StartMetricsUpdater()
		logutil.LogWithBufferf("Prometheus metrics enabled on %s/metrics", config.DefaultMetricsPort)
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
