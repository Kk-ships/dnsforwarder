package dnsresolver

import (
	"dnsloadbalancer/config"
	"dnsloadbalancer/dnssource"
	"dnsloadbalancer/domainrouting"
	"dnsloadbalancer/edns"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/network"
	"time"

	"sync"

	"dnsloadbalancer/metric"

	"github.com/miekg/dns"
)

var (
	metricsRecorder  = metric.MetricsRecorderInstance
	dnsUsageStats    = make(map[string]int)
	statsMutex       sync.Mutex
	cacheRefresh     = config.DefaultCacheRefresh
	dnsClient        = &dns.Client{Timeout: config.DefaultDNSTimeout}
	dnsMsgPool       = sync.Pool{New: func() interface{} { return new(dns.Msg) }}
	ednsManager      = edns.NewClientSubnetManager()
	interfaceManager = network.NewInterfaceManager()
)

func UpdateDNSServersCache() {
	// Initialize outbound interface for DNS client
	if dialer, err := interfaceManager.CreateOutboundDialer(); err != nil {
		logutil.Logger.Warnf("Failed to create outbound dialer: %v", err)
	} else {
		dnsClient.Dialer = dialer
		logutil.Logger.Info("DNS client configured with outbound interface")
	}

	// Run initial health check immediately
	dnssource.UpdateDNSServersCache(
		metricsRecorder,
		cacheRefresh,
		config.EnableClientRouting,
		config.PrivateServers,
		config.PublicServers,
		dnsClient,
		&dnsMsgPool,
	)

	ticker := time.NewTicker(cacheRefresh)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			dnssource.UpdateDNSServersCache(
				metricsRecorder,
				cacheRefresh,
				config.EnableClientRouting,
				config.PrivateServers,
				config.PublicServers,
				dnsClient,
				&dnsMsgPool,
			)
		}
	}()
}

func prepareDNSQuery(domain string, qtype uint16) *dns.Msg {
	m := dnsMsgPool.Get().(*dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.Authoritative = false     // We're a forwarder, not authoritative
	m.RecursionAvailable = true // We provide recursion
	m.RecursionDesired = true   // Client expects recursion
	// Set EDNS0 with appropriate buffer size
	if ednsManager.Enabled {
		m.SetEdns0(ednsManager.GetEDNSSize(), false)
	}
	return m
}

func prepareDNSQueryWithClientIP(msg *dns.Msg, _ string) *dns.Msg {
	// Note: EDNS Client Subnet is added per-server in exchangeWithServer
	return prepareDNSQuery(msg.Question[0].Name, msg.Question[0].Qtype)
}

func ResolverForClient(m *dns.Msg, clientIP string) *dns.Msg {
	msg := prepareDNSQueryWithClientIP(m, clientIP)
	defer dnsMsgPool.Put(msg)
	privateServers, publicServers := dnssource.GetServersForClient(clientIP, &dnssource.CacheMutex)
	return upstreamDNSQuery(privateServers, publicServers, msg, clientIP)
}

func ResolverForDomain(m *dns.Msg, clientIP string) *dns.Msg {
	msg := prepareDNSQueryWithClientIP(m, clientIP)
	defer dnsMsgPool.Put(msg)
	if svr, ok := domainrouting.RoutingTable[msg.Question[0].Name]; ok {
		return upstreamDNSQuery([]string{svr}, []string{}, msg, clientIP)
	}
	return ResolverForClient(msg, clientIP)
}

func upstreamDNSQuery(privateServers []string, publicServers []string, m *dns.Msg, clientIP string) *dns.Msg {
	if len(publicServers) == 0 && len(privateServers) == 0 {
		logutil.Logger.Warn("No upstream DNS servers available")
		return nil
	}
	for _, svr := range privateServers {
		answer, err := exchangeWithServerAndClientIP(m, svr, clientIP)
		if err == nil {
			return answer
		}
		logutil.Logger.Warnf("Failed to query private server %s: %v", svr, err)
	}
	if len(publicServers) == 0 {
		logutil.Logger.Warn("No public servers available after trying private servers")
		return nil
	}
	for _, svr := range publicServers {
		answer, err := exchangeWithServerAndClientIP(m, svr, clientIP)
		if err == nil {
			return answer
		}
	}
	return nil
}

func exchangeWithServerAndClientIP(m *dns.Msg, svr string, clientIP string) (*dns.Msg, error) {
	// Create a copy of the message for this specific server
	query := m.Copy()
	// Add EDNS Client Subnet only if this server supports it
	if ednsManager.ShouldAddClientSubnet(svr) && clientIP != "" {
		logutil.Logger.Debugf("Adding EDNS Client Subnet for server %s, clientIP %s", svr, clientIP)
		ednsManager.AddClientSubnet(query, clientIP)
	}
	response, rtt, err := dnsClient.Exchange(query, svr)
	if err == nil && response != nil {
		statsMutex.Lock()
		dnsUsageStats[svr]++
		statsMutex.Unlock()
		// Process EDNS Client Subnet response
		ednsManager.ProcessClientSubnetResponse(response)
		if config.EnableMetrics {
			metricsRecorder.RecordUpstreamQuery(svr, "success", rtt)
		}
		return response, nil
	}
	logutil.Logger.Warnf("Exchange error using server %s: %v", svr, err)
	if config.EnableMetrics {
		metricsRecorder.RecordUpstreamQuery(svr, "error", rtt)
		metricsRecorder.RecordError("upstream_query_failed", "dns_exchange")
	}
	return nil, err
}
