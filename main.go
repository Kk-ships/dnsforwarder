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
	"dnsloadbalancer/metric"
	"dnsloadbalancer/util"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

var cfg = config.Get()

type dnsHandler struct{}

func (h *dnsHandler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	// Use conditional timer to avoid time.Now() overhead when metrics are disabled
	timer := metric.StartDNSQueryTimer(cfg.EnableMetrics)

	// Prepare response message first using pooled DNS message
	msg := dnsresolver.GetDNSMsg()
	defer dnsresolver.PutDNSMsg(msg)
	msg.SetReply(r)
	msg.Authoritative = true

	// Initialize variables for logging/metrics
	var queryType = "unknown"
	var queryStatus = "success" // Status of DNS query resolution
	var clientIP = ""
	var domain = ""

	// Process DNS query and get answers
	if len(r.Question) > 0 {
		q := r.Question[0]
		queryType = dns.TypeToString[q.Qtype]
		clientIP = util.GetClientIP(w)
		domain = q.Name

		answers := cache.ResolverWithCache(
			q.Name, q.Qtype, clientIP,
		)
		if answers != nil {
			msg.Answer = answers
		} else {
			queryStatus = "no_answers" // DNS resolution didn't find answers
		}
	} else {
		queryStatus = "no_questions" // Invalid DNS query format
	}

	// Send DNS response immediately
	writeErr := w.WriteMsg(msg)

	// Handle metrics synchronously for better performance
	if cfg.EnableMetrics {
		duration := timer.Elapsed()
		// Use the combined method to record DNS query, device IP, and domain in one call
		metric.GetFastMetricsInstance().FastRecordDNSQueryWithDeviceIPAndDomain(queryType, queryStatus, clientIP, domain, duration)

		// Handle response writing error logging
		if writeErr != nil {
			metric.GetFastMetricsInstance().FastRecordError("dns_response_write_failed", "dns_handler")
		}
	}

	// Handle error logging immediately if needed
	if writeErr != nil {
		logutil.Logger.Errorf("Failed to write DNS response: %v", writeErr)
	}
}

// --- Server Startup ---
func StartDNSServer() {
	logutil.Logger.Debug("StartDNSServer: start")
	defer logutil.Logger.Debug("StartDNSServer: end")
	// --- Initialization ---
	cache.Init(cfg.CacheTTL, cfg.EnableMetrics, metric.GetFastMetricsInstance(), cfg.EnableClientRouting, cfg.EnableDomainRouting)
	logutil.Logger.Debug("StartDNSServer: cache initialized")
	dnssource.InitDNSSource(metric.GetFastMetricsInstance())
	logutil.Logger.Debug("StartDNSServer: dns source initialized")
	dnsresolver.UpdateDNSServersCache()
	logutil.Logger.Debug("StartDNSServer: dns servers cache updated")
	logutil.Logger.Infof("DNS servers cache updated with private: %v, public: %v", dnssource.PrivateServersCache, dnssource.PublicServersCache)
	domainrouting.InitializeDomainRouting()
	logutil.Logger.Debug("StartDNSServer: domain routing initialized")
	clientrouting.InitializeClientRouting()
	logutil.Logger.Debug("StartDNSServer: client routing initialized")

	// Start background services (each already spawns goroutines internally)
	cache.StartCacheStatsLogger()
	if cfg.EnableMetrics {
		metric.StartMetricsServer()
		metric.StartMetricsUpdater()
		logutil.Logger.Infof("Prometheus metrics enabled on %s/metrics", cfg.MetricsPort)
	}

	// DNS server setup
	handler := new(dnsHandler)
	server := &dns.Server{
		Addr:      cfg.DNSPort,
		Net:       "udp",
		Handler:   handler,
		UDPSize:   cfg.UDPSize,
		ReusePort: true,
	}
	logutil.Logger.Infof("Starting DNS server on port %s (UDP size: %d bytes)...", cfg.DNSPort, cfg.UDPSize)
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

		// Stop all cache processes and save final state
		cache.Cleanup()

		// Shutdown metrics recording gracefully
		if cfg.EnableMetrics {
			logutil.Logger.Debug("Shutting down metrics recorder...")
			if err := metric.ShutdownGlobalInstance(3 * time.Second); err != nil {
				logutil.Logger.Warnf("Metrics shutdown timed out: %v", err)
				// Force close if graceful shutdown times out
				if closeErr := metric.CloseGlobalInstance(); closeErr != nil {
					logutil.Logger.Errorf("Failed to force close metrics instance: %v", closeErr)
				}
			} else {
				logutil.Logger.Debug("Metrics recorder shut down gracefully")
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.ShutdownContext(ctx); err != nil {
			logutil.Logger.Errorf("Graceful shutdown failed: %v", err)
		} else {
			logutil.Logger.Infof("DNS server shut down gracefully")
		}

		// Flush any remaining log messages
		logutil.Logger.Flush()

	case err := <-errCh:
		if err != nil {
			logutil.Logger.Fatalf("Failed to start server: %s\n", err.Error())
		}
	}
}

func main() {
	logutil.Logger.Debug("main: start")
	StartDNSServer()
	logutil.Logger.Debug("main: end")
	// Ensure all logs are flushed before exit
	logutil.Logger.Flush()
}
