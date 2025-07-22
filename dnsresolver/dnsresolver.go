package dnsresolver

import (
	"dnsloadbalancer/clientrouting"
	"dnsloadbalancer/config"
	"dnsloadbalancer/dnssource"
	"dnsloadbalancer/domainrouting"
	"dnsloadbalancer/logutil"

	"sync"
	"time"

	"dnsloadbalancer/metric"

	"github.com/miekg/dns"
)

var (
	metricsRecorder = metric.MetricsRecorderInstance
	dnsUsageStats   = make(map[string]int)
	statsMutex      sync.Mutex
	cacheTTL        = config.DefaultCacheTTL
	dnsClient       = &dns.Client{Timeout: config.DefaultDNSTimeout}
	dnsMsgPool      = sync.Pool{New: func() interface{} { return new(dns.Msg) }}
)

func UpdateDNSServersCache() {
	dnssource.UpdateDNSServersCache(
		metric.MetricsRecorderInstance,
		cacheTTL,
		config.EnableClientRouting,
		config.PrivateServers,
		config.PublicServers,
		dnsClient,
		&dnsMsgPool,
	)
}

func getCachedDNSServers() []string {
	var servers []string
	dnssource.CacheMutex.RLock()
	cacheValid := time.Since(dnssource.CacheLastUpdated) <= cacheTTL && len(dnssource.ReachableServersCache) > 0
	servers = append([]string(nil), dnssource.ReachableServersCache...)
	dnssource.CacheMutex.RUnlock()

	if cacheValid {
		return servers
	}
	UpdateDNSServersCache()
	dnssource.CacheMutex.RLock()
	servers = append([]string(nil), dnssource.ReachableServersCache...)
	dnssource.CacheMutex.RUnlock()
	return servers
}

func ResolverForClient(domain string, qtype uint16, clientIP string) []dns.RR {
	m := dnsMsgPool.Get().(*dns.Msg)
	defer dnsMsgPool.Put(m)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true
	var servers []string
	// Prefer client-specific servers if clientIP is available
	// This allows client routing to take precedence over the general server cache.
	// If client routing is not enabled or no specific servers are found, fallback to cached servers
	// or default servers.
	if clientIP != "" {
		servers = clientrouting.GetServersForClient(clientIP, &dnssource.CacheMutex)
		if servers == nil {
			servers = getCachedDNSServers()
		}
	} else {
		servers = getCachedDNSServers()
	}
	return upstreamDNSQuery(servers, m)
}

func ResolverForDomain(domain string, qtype uint16, clientIP string) []dns.RR {
	m := dnsMsgPool.Get().(*dns.Msg)
	defer dnsMsgPool.Put(m)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true
	if svr, ok := domainrouting.RoutingTable[domain]; ok {
		return upstreamDNSQuery([]string{svr}, m)
	}
	return ResolverForClient(domain, qtype, clientIP)
}

func upstreamDNSQuery(servers []string, m *dns.Msg) []dns.RR {
	if len(servers) == 0 {
		logutil.LogWithBufferf("[ERROR] No upstream DNS servers available")
		return nil
	}
	for _, svr := range servers {
		response, rtt, err := dnsClient.Exchange(m, svr)
		if err == nil && response != nil {
			statsMutex.Lock()
			dnsUsageStats[svr]++
			statsMutex.Unlock()
			if config.EnableMetrics {
				metricsRecorder.RecordUpstreamQuery(svr, "success", rtt)
			}
			return response.Answer
		}
		logutil.LogWithBufferf("[WARNING] exchange error using server %s: %v", svr, err)
		if config.EnableMetrics {
			metricsRecorder.RecordUpstreamQuery(svr, "error", rtt)
			metricsRecorder.RecordError("upstream_query_failed", "dns_exchange")
		}
	}
	return nil
}
