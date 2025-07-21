package main

import (
	"context"
	"dnsloadbalancer/cache"
	"dnsloadbalancer/clientrouting"
	"dnsloadbalancer/config"
	"dnsloadbalancer/dnssource"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/util"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

var (
	dnsUsageStats = make(map[string]int)
	statsMutex    sync.Mutex

	cacheTTL   = config.DefaultCacheTTL
	dnsClient  = &dns.Client{Timeout: config.DefaultDNSTimeout}
	dnsMsgPool = sync.Pool{New: func() interface{} { return new(dns.Msg) }}
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
		servers = clientrouting.GetServersForClient(clientIP, &dnssource.CacheMutex)
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

func updateDNSServersCache() {
	dnssource.UpdateDNSServersCache(
		metricsRecorder,
		cacheTTL,
		config.EnableClientRouting,
		config.PrivateServers,
		config.PublicServers,
		dnsClient,
		&dnsMsgPool,
	)
}

func getCachedDNSServers() []string {
	dnssource.CacheMutex.RLock()
	servers := make([]string, len(dnssource.ReachableServersCache))
	copy(servers, dnssource.ReachableServersCache)
	cacheValid := time.Since(dnssource.CacheLastUpdated) <= cacheTTL && len(servers) > 0
	dnssource.CacheMutex.RUnlock()

	if cacheValid {
		return servers
	}

	// Update cache and try again
	updateDNSServersCache()

	dnssource.CacheMutex.RLock()
	servers = make([]string, len(dnssource.ReachableServersCache))
	copy(servers, dnssource.ReachableServersCache)
	dnssource.CacheMutex.RUnlock()
	return servers
}

// --- DNS Resolver ---
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
	// --- Initialization ---
	cache.Init(config.DefaultDNSCacheTTL, config.EnableMetrics, metricsRecorder, config.EnableClientRouting)
	clientrouting.InitializeClientRouting()
	updateDNSServersCache()

	// Start cache stats and metrics logging in background
	go cache.StartCacheStatsLogger()
	if config.EnableMetrics {
		go StartMetricsServer()
		go StartMetricsUpdater()
		logutil.LogWithBufferf("Prometheus metrics enabled on %s/metrics", config.DefaultMetricsPort)
	}

	// DNS server setup
	handler := new(dnsHandler)
	server := &dns.Server{
		Addr:      config.DefaultDNSPort,
		Net:       "udp",
		Handler:   handler,
		UDPSize:   config.DefaultUDPSize,
		ReusePort: true,
	}

	logutil.LogWithBufferf("Starting DNS server on port 53 (UDP)")
	if config.EnableClientRouting {
		logutil.LogWithBufferf("Client-based DNS routing enabled")
	}
	// --- Server Execution ---
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	// Graceful shutdown on signal
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
