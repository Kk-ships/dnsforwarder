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
	"dnsloadbalancer/ratelimit"
	"dnsloadbalancer/util"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

var cfg = config.Get()

// rateLimiter will be initialized after rate limiting is set up
var rateLimiter *ratelimit.RateLimiter

var metricRecorder *metric.FastMetricsRecorder

// Counter for periodically recording suspicion metrics to avoid overhead
var suspicionMetricsCounter uint64

type dnsHandler struct{}

// handleEmptyQuery handles DNS queries with no questions
func (h *dnsHandler) handleEmptyQuery(w dns.ResponseWriter, r *dns.Msg, timer metric.ConditionalTimer) {
	msg := dnsresolver.DnsMsgPool.Get().(*dns.Msg)
	defer dnsresolver.DnsMsgPool.Put(msg)

	msg.SetReply(r)
	msg.Authoritative = true

	h.finalizeDNSResponse(w, msg, timer, "unknown", "no_questions", "", "")
}

// sendRateLimitResponse sends a SERVFAIL response for rate-limited requests
func (h *dnsHandler) sendRateLimitResponse(w dns.ResponseWriter, r *dns.Msg, timer metric.ConditionalTimer, clientIP, domain string) {
	msg := dnsresolver.DnsMsgPool.Get().(*dns.Msg)
	defer dnsresolver.DnsMsgPool.Put(msg)

	msg.SetReply(r)
	msg.Rcode = dns.RcodeServerFailure

	if cfg.EnableMetrics {
		duration := timer.Elapsed()
		metricRecorder.FastRecordDNSQueryWithDeviceIPAndDomain("rate_limited", "blocked", clientIP, domain, duration)
	}

	if err := w.WriteMsg(msg); err != nil {
		logutil.Logger.Errorf("Failed to write rate limit response: %v", err)
	}
}

// finalizeDNSResponse handles metrics recording and response writing
func (h *dnsHandler) finalizeDNSResponse(w dns.ResponseWriter, msg *dns.Msg, timer metric.ConditionalTimer, queryType, queryStatus, clientIP, domain string) {
	// Record DNS query metric with device IP and domain tracking
	if cfg.EnableMetrics {
		duration := timer.Elapsed()
		metricRecorder.FastRecordDNSQueryWithDeviceIPAndDomain(queryType, queryStatus, clientIP, domain, duration)
	}

	// Handle response writing separately from query metrics
	if err := w.WriteMsg(msg); err != nil {
		if cfg.EnableMetrics {
			metricRecorder.FastRecordError("dns_response_write_failed", "dns_handler")
		}
		logutil.Logger.Errorf("Failed to write DNS response: %v", err)
	}
}

func (h *dnsHandler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	// Use conditional timer to avoid time.Now() overhead when metrics are disabled
	timer := metric.StartDNSQueryTimer(cfg.EnableMetrics)

	// Fast path: check if we have questions first
	if len(r.Question) == 0 {
		h.handleEmptyQuery(w, r, timer)
		return
	}

	// Extract client info and question details early
	clientIP := util.GetClientIP(w)
	q := r.Question[0]
	queryType := dns.TypeToString[q.Qtype]
	domain := q.Name

	// Rate limiting check (optimized to avoid redundant conditions)
	if rateLimiter != nil {
		if result := rateLimiter.CheckRateLimit(clientIP); !result.Allowed {
			logutil.Logger.Warnf("Rate limit exceeded for client %s: %s", clientIP, result.Reason)

			// Record rate limiting blocked metric
			if cfg.EnableMetrics {
				metricRecorder.FastRecordRateLimitBlocked(clientIP, result.Reason)

				// Record suspicion level for blocked clients
				if clientStats := rateLimiter.GetClientStats(clientIP); clientStats["exists"].(bool) {
					suspicionLevel := clientStats["suspicion_level"].(int32)
					metricRecorder.FastSetRateLimitSuspiciousClient(clientIP, float64(suspicionLevel))
				}
			}

			h.sendRateLimitResponse(w, r, timer, clientIP, domain)
			return
		} else {
			// Record rate limiting allowed metric
			if cfg.EnableMetrics {
				metricRecorder.FastRecordRateLimitAllowed(clientIP)

				// Periodically record suspicion levels for active clients (every 100 requests to avoid overhead)
				// Use a simple counter to reduce frequency
				if atomic.AddUint64(&suspicionMetricsCounter, 1)%100 == 0 {
					if clientStats := rateLimiter.GetClientStats(clientIP); clientStats["exists"].(bool) {
						suspicionLevel := clientStats["suspicion_level"].(int32)
						if suspicionLevel > 0 { // Only record if there's some suspicion
							metricRecorder.FastSetRateLimitSuspiciousClient(clientIP, float64(suspicionLevel))
						}
					}
				}
			}
		}
	}

	logutil.Logger.Debugf("ServeDNS: clientIP=%s, q.Name=%s, q.Qtype=%d", clientIP, domain, q.Qtype)

	// Create response message from pool
	msg := dnsresolver.DnsMsgPool.Get().(*dns.Msg)
	defer dnsresolver.DnsMsgPool.Put(msg)

	msg.SetReply(r)
	msg.Authoritative = true

	// Resolve DNS query
	var queryStatus string = "success"
	if answers := cache.ResolverWithCache(domain, q.Qtype, clientIP); answers != nil {
		msg.Answer = answers
		logutil.Logger.Debugf("ServeDNS: got answers for %s", domain)
	} else {
		queryStatus = "no_answers"
	}

	// Record metrics and send response
	h.finalizeDNSResponse(w, msg, timer, queryType, queryStatus, clientIP, domain)
}

// --- Server Startup ---
func StartDNSServer() {
	logutil.Logger.Debug("StartDNSServer: start")
	defer logutil.Logger.Debug("StartDNSServer: end")
	// --- Initialization ---
	if cfg.EnableMetrics {
		metricRecorder = metric.GetFastMetricsInstance()
		go metric.StartMetricsServer()
		go metric.StartMetricsUpdater()
		logutil.Logger.Infof("Prometheus metrics enabled on %s/metrics", cfg.MetricsPort)
	}
	cache.Init(cfg.CacheTTL, cfg.EnableMetrics, metricRecorder, cfg.EnableClientRouting, cfg.EnableDomainRouting)
	logutil.Logger.Debug("StartDNSServer: cache initialized")
	dnssource.InitDNSSource(metricRecorder)
	logutil.Logger.Debug("StartDNSServer: dns source initialized")
	dnsresolver.UpdateDNSServersCache()
	logutil.Logger.Debug("StartDNSServer: dns servers cache updated")
	logutil.Logger.Infof("DNS servers cache updated with private: %v, public: %v", dnssource.PrivateServersCache, dnssource.PublicServersCache)
	domainrouting.InitializeDomainRouting()
	logutil.Logger.Debug("StartDNSServer: domain routing initialized")
	clientrouting.InitializeClientRouting()
	logutil.Logger.Debug("StartDNSServer: client routing initialized")
	// Initialize rate limiting if enabled
	if cfg.EnableRateLimit {
		ratelimit.InitializeGlobalRateLimiter(true)
		rateLimiter = ratelimit.GetGlobalRateLimiter() // Cache the rate limiter for performance
		logutil.Logger.Debug("StartDNSServer: rate limiting initialized")
	}
	// Start cache stats and metrics logging in background
	cache.StartCacheStatsLogger()

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

		// Stop cache persistence and save final state
		cache.StopCachePersistence()

		// Shutdown rate limiter if enabled
		if cfg.EnableRateLimit {
			ratelimit.ShutdownGlobalRateLimiter()
			logutil.Logger.Debug("Rate limiter shut down gracefully")
		}

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
