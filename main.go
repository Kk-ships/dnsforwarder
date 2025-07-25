package main

import (
	"context"
	"dnsloadbalancer/cache"
	"dnsloadbalancer/clientrouting"
	"dnsloadbalancer/config"
	"dnsloadbalancer/dnsresolver"
	"dnsloadbalancer/dnssource"
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
	logutil.Logger.Debug("ServeDNS: start")
	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true

	if len(r.Question) > 0 {
		q := r.Question[0]
		clientIP := util.GetClientIP(w)
		logutil.Logger.Debugf("ServeDNS: clientIP=%s, q.Name=%s, q.Qtype=%d", clientIP, q.Name, q.Qtype)
		answers := cache.ResolverWithCache(
			q.Name, q.Qtype, clientIP,
		)
		if answers != nil {
			msg.Answer = answers
			logutil.Logger.Debugf("ServeDNS: got answers for %s", q.Name)
		}
	}

	if err := w.WriteMsg(msg); err != nil {
		if config.EnableMetrics {
			metricsRecorder.RecordError("dns_response_write_failed", "dns_handler")
		}
		logutil.Logger.Errorf("Failed to write DNS response: %v", err)
	}
	logutil.Logger.Debug("ServeDNS: end")
}

// --- Server Startup ---
func StartDNSServer() {
	logutil.Logger.Debug("StartDNSServer: start")
	// --- Initialization ---
	cache.Init(config.DefaultDNSCacheTTL, config.EnableMetrics, metricsRecorder, config.EnableClientRouting, config.EnableDomainRouting)
	logutil.Logger.Debug("StartDNSServer: cache initialized")
	dnssource.InitDNSSource(metricsRecorder)
	logutil.Logger.Debug("StartDNSServer: dns source initialized")
	dnsresolver.UpdateDNSServersCache()
	logutil.Logger.Debug("StartDNSServer: dns servers cache updated")
	logutil.Logger.Infof("DNS servers cache updated with private: %v, public: %v", dnssource.PrivateServersCache, dnssource.PublicServersCache)
	domainrouting.InitializeDomainRouting()
	logutil.Logger.Debug("StartDNSServer: domain routing initialized")
	clientrouting.InitializeClientRouting()
	logutil.Logger.Debug("StartDNSServer: client routing initialized")
	// Start cache stats and metrics logging in background
	cache.StartCacheStatsLogger()
	if config.EnableMetrics {
		go StartMetricsServer()
		go StartMetricsUpdater()
		logutil.Logger.Infof("Prometheus metrics enabled on %s/metrics", config.DefaultMetricsPort)
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
	logutil.Logger.Infof("Starting DNS server on port 53 (UDP)")
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
		logutil.Logger.Infof("Received signal %s, shutting down DNS server gracefully...", sig)

		// Stop cache persistence and save final state
		cache.StopCachePersistence()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.ShutdownContext(ctx); err != nil {
			logutil.Logger.Errorf("Graceful shutdown failed: %v", err)
		} else {
			logutil.Logger.Infof("DNS server shut down gracefully")
		}
	case err := <-errCh:
		if err != nil {
			logutil.Logger.Fatalf("Failed to start server: %s\n", err.Error())
		}
	}
	logutil.Logger.Debug("StartDNSServer: end")
}

func main() {
	logutil.Logger.Debug("main: start")
	StartDNSServer()
	logutil.Logger.Debug("main: end")
}
