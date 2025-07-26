package main

import (
	"context"
	"dnsloadbalancer/cache"
	"dnsloadbalancer/clientrouting"
	"dnsloadbalancer/config"
	"dnsloadbalancer/dnsresolver"
	"dnsloadbalancer/dnssource"
	"dnsloadbalancer/domainrouting"
	"dnsloadbalancer/edns"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/network"
	"dnsloadbalancer/pidfile"
	"dnsloadbalancer/util"
	"net"

	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

type dnsHandler struct {
	ednsManager *edns.ClientSubnetManager
}

func (h *dnsHandler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	logutil.Logger.Debug("ServeDNS: start")
	defer logutil.Logger.Debug("ServeDNS: end")

	msg := new(dns.Msg)
	msg.SetReply(r)
	if len(r.Question) == 0 {
		logutil.Logger.Warn("ServeDNS: no question in request")
		return
	}

	q := r.Question[0]
	clientIP := util.GetClientIP(w)

	// Extract real client IP for EDNS Client Subnet
	if h.ednsManager.Enabled {
		if realClientIP := h.ednsManager.ExtractClientIP(w); realClientIP != "" {
			clientIP = realClientIP
		}
		if !util.IsPrivateIP(net.ParseIP(clientIP)) {
			logutil.Logger.Info("ServeDNS: using real client IP for EDNS Client Subnet:", clientIP)
			for _, extra := range r.Extra {
				if o, ok := extra.(*dns.OPT); ok {
					newOpt := &dns.OPT{
						Hdr: dns.RR_Header{
							Name:   ".",
							Rrtype: dns.TypeOPT,
						},
						Option: o.Option,
					}
					newOpt.SetUDPSize(o.UDPSize())
					msg.Extra = append(msg.Extra, newOpt)
				}
			}
		}
	}

	logutil.Logger.Debugf("ServeDNS: clientIP=%s, q.Name=%s, q.Qtype=%d", clientIP, q.Name, q.Qtype)
	answers := cache.ResolverWithCache(msg, clientIP)

	if answers == nil {
		logutil.Logger.Warnf("ServeDNS: no answers found for %s (qtype=%d)", q.Name, q.Qtype)
	} else {
		msg.Answer = answers.Answer
		msg.Ns = answers.Ns
		msg.Extra = append(msg.Extra, answers.Extra...)
		logutil.Logger.Debugf("ServeDNS: got %d answers for %s", len(answers.Answer), q.Name)
	}

	if err := w.WriteMsg(msg); err != nil {
		logError(err, "dns_response_write_failed", "dns_handler")
	}
}

func logError(err error, metricKey, source string) {
	if config.EnableMetrics {
		metricsRecorder.RecordError(metricKey, source)
	}
	logutil.Logger.Errorf("Failed to write DNS response: %v", err)
}

// --- Server Startup ---
func StartDNSServer() {
	logutil.Logger.Debug("StartDNSServer: start")

	// --- PID File Management ---
	var pid *pidfile.PIDFile
	if config.EnablePIDFile {
		pid = pidfile.New(config.PIDFilePath)
		if err := pid.Write(); err != nil {
			logutil.Logger.Fatalf("Failed to create PID file: %v", err)
		}
		logutil.Logger.Infof("PID file created: %s", config.PIDFilePath)

		// Ensure PID file is removed on exit
		defer func() {
			if err := pid.Remove(); err != nil {
				logutil.Logger.Errorf("Failed to remove PID file: %v", err)
			}
		}()
	}

	// --- Network Interface Management ---
	interfaceManager := network.NewInterfaceManager()

	// Validate interfaces if binding is enabled
	if err := interfaceManager.ValidateInterfaces(); err != nil {
		logutil.Logger.Fatalf("Interface validation failed: %v", err)
	}

	// Test socket binding capabilities
	if config.BindToInterface {
		capabilities := interfaceManager.GetInterfaceCapabilities()
		logutil.Logger.Infof("Interface binding capabilities: %+v", capabilities)

		if err := interfaceManager.TestSocketBinding(); err != nil {
			logutil.Logger.Warnf("Socket binding test failed: %v", err)
			logutil.Logger.Warn("Interface binding may not work properly. Consider running as root or disabling interface binding.")
		}
	}

	// List available interfaces for debugging
	if config.BindToInterface {
		interfaceManager.ListAvailableInterfaces()
	} // Get listen addresses based on interface configuration
	listenAddresses, err := interfaceManager.GetListenAddresses()
	if err != nil {
		logutil.Logger.Fatalf("Failed to get listen addresses: %v", err)
	}

	// --- Initialization ---
	cache.Init(config.DefaultDNSCacheTTL, config.EnableMetrics, metricsRecorder, config.EnableClientRouting, config.EnableDomainRouting)
	logutil.Logger.Debug("StartDNSServer: cache initialized")
	dnssource.InitDNSSource(metricsRecorder)
	logutil.Logger.Debug("StartDNSServer: dns source initialized")
	dnsresolver.UpdateDNSServersCache()
	logutil.Logger.Debug("StartDNSServer: dns servers cache updated")
	logutil.Logger.Infof("DNS servers cache updated with private: %v, public: %v", dnssource.PrivateServersCache, dnssource.PublicServersCache)

	// Debug: Check if DNS servers are properly configured
	logutil.Logger.Infof("Configuration - Private DNS servers: %v", config.PrivateServers)
	logutil.Logger.Infof("Configuration - Public DNS servers: %v", config.PublicServers)
	logutil.Logger.Infof("Configuration - Client routing enabled: %v", config.EnableClientRouting)
	logutil.Logger.Infof("Configuration - Domain routing enabled: %v", config.EnableDomainRouting)
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

	// DNS server setup with EDNS support
	ednsManager := edns.NewClientSubnetManager()
	handler := &dnsHandler{ednsManager: ednsManager}

	// Create DNS servers for each listen address
	var servers []*dns.Server
	errCh := make(chan error, len(listenAddresses))

	for i, addr := range listenAddresses {
		// Determine which interface to bind to for this address
		var interfaceName string
		if config.BindToInterface && len(config.ListenInterfaces) > 0 {
			if i < len(config.ListenInterfaces) {
				interfaceName = config.ListenInterfaces[i]
			} else {
				interfaceName = config.ListenInterfaces[0] // Use first interface as fallback
			}
		}

		server := &dns.Server{
			Addr:      addr,
			Net:       "udp",
			Handler:   handler,
			UDPSize:   config.DefaultUDPSize,
			ReusePort: true,
		}

		// If interface binding is enabled, create custom packet connection
		if config.BindToInterface && interfaceName != "" {
			conn, err := interfaceManager.CreateBoundUDPListener(addr, interfaceName)
			if err != nil {
				logutil.Logger.Errorf("Failed to create bound UDP listener for interface %s: %v", interfaceName, err)
				logutil.Logger.Infof("Falling back to standard UDP listener for %s", addr)
			} else {
				server.PacketConn = conn
				logutil.Logger.Infof("Created DNS server on %s bound to interface %s", addr, interfaceName)
			}
		}

		servers = append(servers, server)

		logutil.Logger.Infof("Starting DNS server on %s (UDP)", addr)

		// Start server in goroutine
		go func(srv *dns.Server, iface string) {
			if srv.PacketConn != nil {
				// Use existing connection
				errCh <- srv.ActivateAndServe()
			} else {
				// Standard listen and serve
				errCh <- srv.ListenAndServe()
			}
		}(server, interfaceName)
	}

	// --- Server Execution ---
	// Graceful shutdown on signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logutil.Logger.Infof("Received signal %s, shutting down DNS servers gracefully...", sig)

		// Stop cache persistence and save final state
		cache.StopCachePersistence()

		// Shutdown all servers
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		for _, server := range servers {
			if err := server.ShutdownContext(ctx); err != nil {
				logutil.Logger.Errorf("Graceful shutdown failed for server %s: %v", server.Addr, err)
			} else {
				logutil.Logger.Infof("DNS server %s shut down gracefully", server.Addr)
			}
		}
		// Flush any remaining log messages
		logutil.Logger.Flush()
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
	// Ensure all logs are flushed before exit
	logutil.Logger.Flush()
}
