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

	logutil.Logger.Debug("ServeDNS: start")
	defer logutil.Logger.Debug("ServeDNS: end")

	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true

	var queryType = "unknown"
	var queryStatus = "success" // Status of DNS query resolution
	var clientIP = ""
	var domain = ""
	if len(r.Question) > 0 {
		q := r.Question[0]
		queryType = dns.TypeToString[q.Qtype]
		clientIP = util.GetClientIP(w)
		domain = q.Name
		logutil.Logger.Debugf("ServeDNS: clientIP=%s, q.Name=%s, q.Qtype=%d", clientIP, q.Name, q.Qtype)
		answers := cache.ResolverWithCache(
			q.Name, q.Qtype, clientIP,
		)
		if answers != nil {
			msg.Answer = answers
			logutil.Logger.Debugf("ServeDNS: got answers for %s", q.Name)
		} else {
			queryStatus = "no_answers" // DNS resolution didn't find answers
		}
	} else {
		queryStatus = "no_questions" // Invalid DNS query format
	}

	// Record DNS query metric with device IP and domain tracking
	if cfg.EnableMetrics {
		duration := timer.Elapsed()
		// Use the combined method to record DNS query, device IP, and domain in one call
		metric.GetFastMetricsInstance().FastRecordDNSQueryWithDeviceIPAndDomain(queryType, queryStatus, clientIP, domain, duration)
	}

	// Handle response writing separately from query metrics
	if err := w.WriteMsg(msg); err != nil {
		if cfg.EnableMetrics {
			// Record write failure as a separate metric
			metric.GetFastMetricsInstance().FastRecordError("dns_response_write_failed", "dns_handler")
		}
		logutil.Logger.Errorf("Failed to write DNS response: %v", err)
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
	// Start cache stats and metrics logging in background
	cache.StartCacheStatsLogger()
	if cfg.EnableMetrics {
		go metric.StartMetricsServer()
		go metric.StartMetricsUpdater()
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
