package dnsresolver

import (
	"dnsloadbalancer/config"
	"dnsloadbalancer/dnssource"
	"dnsloadbalancer/domainrouting"
	"dnsloadbalancer/logutil"
	"time"

	"sync"

	"dnsloadbalancer/metric"

	"github.com/miekg/dns"
)

var (
	metricsRecorder = metric.MetricsRecorderInstance
	dnsUsageStats   = make(map[string]int)
	statsMutex      sync.Mutex
	dnsMsgPool      = sync.Pool{New: func() interface{} { return new(dns.Msg) }}
	cfg             = config.Get()
)

func UpdateDNSServersCache() {
	cacheRefresh := cfg.CacheRefresh
	dnsClient := &dns.Client{Timeout: cfg.DNSTimeout}

	ticker := time.NewTicker(cacheRefresh)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			dnssource.UpdateDNSServersCache(
				metricsRecorder,
				cacheRefresh,
				cfg.EnableClientRouting,
				cfg.PrivateServers,
				cfg.PublicServers,
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
	return m
}

func ResolverForClient(domain string, qtype uint16, clientIP string) []dns.RR {
	m := prepareDNSQuery(domain, qtype)
	defer dnsMsgPool.Put(m)
	privateServers, publicServers := dnssource.GetServersForClient(clientIP, &dnssource.CacheMutex)
	result := upstreamDNSQuery(privateServers, publicServers, m)
	return result
}

func ResolverForDomain(domain string, qtype uint16, clientIP string) []dns.RR {
	m := prepareDNSQuery(domain, qtype)
	defer dnsMsgPool.Put(m)
	if svr, ok := domainrouting.RoutingTable[domain]; ok {
		result := upstreamDNSQuery([]string{svr}, []string{}, m)
		return result
	}
	result := ResolverForClient(domain, qtype, clientIP)
	return result
}

func upstreamDNSQuery(privateServers []string, publicServers []string, m *dns.Msg) []dns.RR {
	if len(publicServers) == 0 && len(privateServers) == 0 {
		logutil.Logger.Warn("No upstream DNS servers available")
		return nil
	}
	for _, svr := range privateServers {
		answer, err := exchangeWithServer(m, svr)
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
		answer, err := exchangeWithServer(m, svr)
		if err == nil {
			return answer
		}
	}
	return nil
}

func exchangeWithServer(m *dns.Msg, svr string) ([]dns.RR, error) {
	dnsClient := &dns.Client{Timeout: cfg.DNSTimeout}
	response, rtt, err := dnsClient.Exchange(m, svr)
	if err == nil && response != nil {
		statsMutex.Lock()
		dnsUsageStats[svr]++
		statsMutex.Unlock()
		if cfg.EnableMetrics {
			metricsRecorder.RecordUpstreamQuery(svr, "success", rtt)
		}
		return response.Answer, nil
	}
	logutil.Logger.Warnf("Exchange error using server %s: %v", svr, err)
	if cfg.EnableMetrics {
		metricsRecorder.RecordUpstreamQuery(svr, "error", rtt)
		metricsRecorder.RecordError("upstream_query_failed", "dns_exchange")
	}
	return nil, err
}
