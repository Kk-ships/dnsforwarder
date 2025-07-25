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
	m.RecursionDesired = true

	// Set EDNS0 with appropriate buffer size
	if ednsManager.Enabled {
		m.SetEdns0(ednsManager.GetEDNSSize(), false)
	}

	return m
}

func prepareDNSQueryWithClientIP(domain string, qtype uint16, _ string) *dns.Msg {
	m := prepareDNSQuery(domain, qtype)
	// Note: EDNS Client Subnet is added per-server in exchangeWithServer
	return m
}

func ResolverForClient(domain string, qtype uint16, clientIP string) []dns.RR {
	m := prepareDNSQueryWithClientIP(domain, qtype, clientIP)
	defer dnsMsgPool.Put(m)
	privateServers, publicServers := dnssource.GetServersForClient(clientIP, &dnssource.CacheMutex)
	result := upstreamDNSQuery(privateServers, publicServers, m, clientIP)
	return result
}

func ResolverForDomain(domain string, qtype uint16, clientIP string) []dns.RR {
	m := prepareDNSQueryWithClientIP(domain, qtype, clientIP)
	defer dnsMsgPool.Put(m)
	if svr, ok := domainrouting.RoutingTable[domain]; ok {
		result := upstreamDNSQuery([]string{svr}, []string{}, m, clientIP)
		return result
	}
	result := ResolverForClient(domain, qtype, clientIP)
	return result
}

func upstreamDNSQuery(privateServers []string, publicServers []string, m *dns.Msg, clientIP string) []dns.RR {
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

func exchangeWithServerAndClientIP(m *dns.Msg, svr string, clientIP string) ([]dns.RR, error) {
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
		return response.Answer, nil
	}
	logutil.Logger.Warnf("Exchange error using server %s: %v", svr, err)
	if config.EnableMetrics {
		metricsRecorder.RecordUpstreamQuery(svr, "error", rtt)
		metricsRecorder.RecordError("upstream_query_failed", "dns_exchange")
	}
	return nil, err
}
