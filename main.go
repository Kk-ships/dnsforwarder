package main

import (
	"context"
	"dnsloadbalancer/cache"
	"dnsloadbalancer/clientrouting"
	"dnsloadbalancer/config"
	"dnsloadbalancer/dnsresolver"
	"dnsloadbalancer/domainrouting"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/util"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

type dnsHandler struct{}

func (h *dnsHandler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true

	if len(r.Question) > 0 {
		q := r.Question[0]
		clientIP := util.GetClientIP(w)
		answers := cache.ResolverWithCache(
			q.Name, q.Qtype, clientIP,
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
	cache.Init(config.DefaultDNSCacheTTL, config.EnableMetrics, metricsRecorder, config.EnableClientRouting, config.EnableDomainRouting)
	// this is blocking since all available upstream cache should be populated before starting server
	dnsresolver.UpdateDNSServersCache()
	domainrouting.InitializeDomainRouting()
	clientrouting.InitializeClientRouting()
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
