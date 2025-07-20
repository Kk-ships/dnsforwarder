package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

// --- Reusable Helpers with Reduced Lock Contention ---

var (
	envDurationCache sync.Map // map[string]time.Duration
	envIntCache      sync.Map // map[string]int
	envStringCache   sync.Map // map[string]string
)

func getEnvDuration(key string, def time.Duration) time.Duration {
	if v, ok := envDurationCache.Load(key); ok {
		return v.(time.Duration)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			val = d
		}
	}
	envDurationCache.Store(key, val)
	return val
}

func getEnvInt(key string, def int) int {
	if v, ok := envIntCache.Load(key); ok {
		return v.(int)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		if i, err := strconv.Atoi(s); err == nil {
			val = i
		}
	}
	envIntCache.Store(key, val)
	return val
}

func getEnvString(key, def string) string {
	if v, ok := envStringCache.Load(key); ok {
		return v.(string)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		val = s
	}
	envStringCache.Store(key, val)
	return val
}

func getEnvStringSlice(key, def string) []string {
	   if v := os.Getenv(key); v != "" {
			   parts := strings.Split(v, ",")
			   out := make([]string, 0, len(parts))
			   for i := range parts {
					   s := strings.TrimSpace(parts[i])
					   if s != "" {
							   out = append(out, s)
					   }
			   }
			   return out
	   }
	   if def == "" {
			   return nil
	   }
	   return []string{def}
}

func getEnvBool(key string, def bool) bool {
	if v, ok := envStringCache.Load(key); ok {
		return v.(string) == "true"
	}
	val := def
	if s := os.Getenv(key); s != "" {
		switch s {
		case "true", "1":
			val = true
		case "false", "0":
			val = false
		}
	}
	envStringCache.Store(key, strconv.FormatBool(val))
	return val
}

// --- Config ---

var (
	defaultCacheTTL    = getEnvDuration("CACHE_TTL", 10*time.Second)
	defaultDNSTimeout  = getEnvDuration("DNS_TIMEOUT", 5*time.Second)
	defaultWorkerCount = getEnvInt("WORKER_COUNT", 5)
	defaultTestDomain  = getEnvString("TEST_DOMAIN", "google.com")
	defaultDNSPort     = getEnvString("DNS_PORT", ":53")
	defaultUDPSize     = getEnvInt("UDP_SIZE", 65535)
	defaultDNSStatslog = getEnvDuration("DNS_STATSLOG", 5*time.Minute)
	defaultDNSServer   = getEnvString("DEFAULT_DNS_SERVER", "8.8.8.8:53")
	defaultCacheSize   = getEnvInt("CACHE_SIZE", 10000)
	defaultDNSCacheTTL = getEnvDuration("DNS_CACHE_TTL", 30*time.Minute)
	defaultMetricsPort = getEnvString("METRICS_PORT", ":8080")
	enableMetrics      = getEnvBool("ENABLE_METRICS", true)

	// Client-based routing configuration
	privateServers      = getEnvStringSlice("PRIVATE_DNS_SERVERS", "192.168.1.1:53")       // Private DNS servers (PiHole, AdGuard, etc.)
	publicServers       = getEnvStringSlice("PUBLIC_DNS_SERVERS", "1.1.1.1:53,8.8.8.8:53") // Public DNS servers (fallback)
	publicOnlyClients   = getEnvStringSlice("PUBLIC_ONLY_CLIENTS", "")                     // Clients that should use public servers only
	enableClientRouting = getEnvBool("ENABLE_CLIENT_ROUTING", false)                       // Enable client-based routing
)

var (
	reachableServersCache []string
	cacheLastUpdated      time.Time
	cacheMutex            sync.RWMutex
	cacheTTL              = defaultCacheTTL
	dnsClient             = &dns.Client{Timeout: defaultDNSTimeout}

	// Client routing cache
	privateServersCache  []string
	publicServersCache   []string
	publicOnlyClientsMap sync.Map // map[string]bool for fast lookup
	privateServersSet    map[string]struct{}
	publicServersSet     map[string]struct{}
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

// --- Log Ring Buffer ---

type LogRingBuffer struct {
	entries []string
	max     int
	idx     int
	full    bool
	sync.Mutex
}

func NewLogRingBuffer(size int) *LogRingBuffer {
	return &LogRingBuffer{
		entries: make([]string, size),
		max:     size,
	}
}

func (l *LogRingBuffer) Add(entry string) {
	l.Lock()
	defer l.Unlock()
	l.entries[l.idx] = entry
	l.idx = (l.idx + 1) % l.max
	if l.idx == 0 {
		l.full = true
	}
}

func (l *LogRingBuffer) GetAll() []string {
	l.Lock()
	defer l.Unlock()
	if !l.full {
		return l.entries[:l.idx]
	}
	result := make([]string, l.max)
	copy(result, l.entries[l.idx:])
	copy(result[l.max-l.idx:], l.entries[:l.idx])
	return result
}

var logBuffer = NewLogRingBuffer(500)

func logWithBufferf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	logBuffer.Add(msg)
	log.Printf(format, v...)
}

func logWithBufferFatalf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	logBuffer.Add(msg)
	log.Fatalf(format, v...)
}

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
			logWithBufferf("[CLIENT-ROUTING] Configured client %s to use public servers only", client)
		}
	}

	logWithBufferf("[CLIENT-ROUTING] Private servers: %v", privateServers)
	logWithBufferf("[CLIENT-ROUTING] Public servers: %v", publicServers)
	logWithBufferf("[CLIENT-ROUTING] Client routing enabled: %v", enableClientRouting)
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
		logWithBufferf("[CLIENT-ROUTING] Client %s using public servers (configured)", clientIP)
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
	return getEnvStringSlice("DNS_SERVERS", defaultDNSServer)
}

func updateDNSServersCache() {
	servers := getDNSServers()
	if len(servers) == 0 || (len(servers) == 1 && servers[0] == "") {
		metricsRecorder.RecordError("no_dns_servers", "config")
		logWithBufferFatalf("[ERROR] no DNS servers found")
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
				logWithBufferf("[WARNING] server %s is not reachable: %v", svr, err)
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
		logWithBufferFatalf("[ERROR] no reachable DNS servers found")
	}

	cacheMutex.Lock()
	reachableServersCache = reachable
	if enableClientRouting {
		privateServersCache = reachablePrivate
		publicServersCache = reachablePublic
		logWithBufferf("[CLIENT-ROUTING] Reachable private servers: %v", reachablePrivate)
		logWithBufferf("[CLIENT-ROUTING] Reachable public servers: %v", reachablePublic)
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
		logWithBufferf("DNS Usage Stats: %+v", dnsUsageStats)
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
		if enableMetrics && len(servers) > 0 {
			// Log which type of servers are being used
			if shouldUsePublicServers(clientIP) {
				logWithBufferf("[CLIENT-ROUTING] Client %s using public servers: %v", clientIP, servers)
			} else {
				logWithBufferf("[CLIENT-ROUTING] Client %s using private+public servers: %v", clientIP, servers)
			}
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

			if enableMetrics {
				metricsRecorder.RecordUpstreamQuery(svr, "success", duration)
			}

			if clientIP != "" && enableClientRouting {
				logWithBufferf("[CLIENT-ROUTING] Client %s resolved %s using server %s", clientIP, domain, svr)
			}

			return response.Answer
		}

		logWithBufferf("[WARNING] exchange error using server %s: %v", svr, err)
		if enableMetrics {
			metricsRecorder.RecordUpstreamQuery(svr, "error", duration)
			metricsRecorder.RecordError("upstream_query_failed", "dns_exchange")
		}
	}

	if enableMetrics {
		metricsRecorder.RecordError("all_upstream_failed", "dns_resolution")
	}
	logWithBufferFatalf("[ERROR] all DNS exchanges failed")
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
		logWithBufferf("[ERROR] Failed to write DNS response: %v", err)
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

	logWithBufferf("Starting DNS server on port 53")
	if enableClientRouting {
		logWithBufferf("Client-based DNS routing enabled")
		logWithBufferf("Private servers: %v", privateServers)
		logWithBufferf("Public servers: %v", publicServers)
		logWithBufferf("Public-only clients: %v", publicOnlyClients)
	}

	startDNSServerCacheUpdater()
	startCacheStatsLogger()

	// Start Prometheus metrics server if enabled
	if enableMetrics {
		StartMetricsServer()
		StartMetricsUpdater()
		logWithBufferf("Prometheus metrics enabled on %s/metrics", defaultMetricsPort)
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
		logWithBufferf("Received signal %s, shutting down DNS server gracefully...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.ShutdownContext(ctx); err != nil {
			logWithBufferf("[ERROR] Graceful shutdown failed: %v", err)
		} else {
			logWithBufferf("DNS server shut down gracefully")
		}
	case err := <-errCh:
		if err != nil {
			logWithBufferFatalf("Failed to start server: %s\n", err.Error())
		}
	}
}

func main() {
	StartDNSServer()
}
