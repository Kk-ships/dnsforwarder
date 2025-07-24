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
	cacheRefresh    = config.DefaultCacheRefresh
	dnsClient       = &dns.Client{Timeout: config.DefaultDNSTimeout}
	dnsMsgPool      = sync.Pool{New: func() interface{} { return new(dns.Msg) }}
)

func UpdateDNSServersCache() {
	logutil.Logger.Debug("UpdateDNSServersCache: start")
	ticker := time.Tick(cacheRefresh)
	for range ticker {
		logutil.Logger.Debug("UpdateDNSServersCache: tick")
		dnssource.UpdateDNSServersCache(
			metricsRecorder,
			cacheRefresh,
			config.EnableClientRouting,
			config.PrivateServers,
			config.PublicServers,
			dnsClient,
			&dnsMsgPool,
		)
		logutil.Logger.Debug("UpdateDNSServersCache: cache updated")
	}
}

func prepareDNSQuery(domain string, qtype uint16) *dns.Msg {
	logutil.Logger.Debugf("prepareDNSQuery: start, domain=%s, qtype=%d", domain, qtype)
	m := dnsMsgPool.Get().(*dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true
	logutil.Logger.Debug("prepareDNSQuery: end")
	return m
}

func ResolverForClient(domain string, qtype uint16, clientIP string) []dns.RR {
	logutil.Logger.Debugf("ResolverForClient: start, domain=%s, qtype=%d, clientIP=%s", domain, qtype, clientIP)
	m := prepareDNSQuery(domain, qtype)
	defer dnsMsgPool.Put(m)
	privateServers, publicServers := dnssource.GetServersForClient(clientIP, &dnssource.CacheMutex)
	logutil.Logger.Debugf("ResolverForClient: privateServers=%v, publicServers=%v", privateServers, publicServers)
	result := upstreamDNSQuery(privateServers, publicServers, m)
	logutil.Logger.Debug("ResolverForClient: end")
	return result
}

func ResolverForDomain(domain string, qtype uint16, clientIP string) []dns.RR {
	logutil.Logger.Debugf("ResolverForDomain: start, domain=%s, qtype=%d, clientIP=%s", domain, qtype, clientIP)
	m := prepareDNSQuery(domain, qtype)
	defer dnsMsgPool.Put(m)
	if svr, ok := domainrouting.RoutingTable[domain]; ok {
		logutil.Logger.Debugf("ResolverForDomain: found routing for domain=%s, server=%s", domain, svr)
		result := upstreamDNSQuery([]string{svr}, []string{}, m)
		logutil.Logger.Debug("ResolverForDomain: end (domain routing)")
		return result
	}
	result := ResolverForClient(domain, qtype, clientIP)
	logutil.Logger.Debug("ResolverForDomain: end (client routing)")
	return result
}

func upstreamDNSQuery(privateServers []string, publicServers []string, m *dns.Msg) []dns.RR {
	logutil.Logger.Debugf("upstreamDNSQuery: start, privateServers=%v, publicServers=%v", privateServers, publicServers)
	if len(publicServers) == 0 && len(privateServers) == 0 {
		logutil.Logger.Warn("No upstream DNS servers available")
		logutil.Logger.Debug("upstreamDNSQuery: end (no servers)")
		return nil
	}
	for _, svr := range privateServers {
		logutil.Logger.Debugf("upstreamDNSQuery: trying private server %s", svr)
		answer, err := exchangeWithServer(m, svr)
		if err == nil {
			logutil.Logger.Debugf("upstreamDNSQuery: got answer from private server %s", svr)
			logutil.Logger.Debug("upstreamDNSQuery: end (private server success)")
			return answer
		}
		logutil.Logger.Warnf("Failed to query private server %s: %v", svr, err)
	}
	if len(publicServers) == 0 {
		logutil.Logger.Warn("No public servers available after trying private servers")
		logutil.Logger.Debug("upstreamDNSQuery: end (no public servers)")
		return nil
	}
	logutil.Logger.Debugf("upstreamDNSQuery: trying public servers: %v", publicServers)
	for _, svr := range publicServers {
		logutil.Logger.Debugf("upstreamDNSQuery: trying public server %s", svr)
		answer, err := exchangeWithServer(m, svr)
		if err == nil {
			logutil.Logger.Debugf("upstreamDNSQuery: got answer from public server %s", svr)
			logutil.Logger.Debug("upstreamDNSQuery: end (public server success)")
			return answer
		}
	}
	logutil.Logger.Debug("upstreamDNSQuery: end (all servers failed)")
	return nil
}

func exchangeWithServer(m *dns.Msg, svr string) ([]dns.RR, error) {
	logutil.Logger.Debugf("exchangeWithServer: start, server=%s", svr)
	response, rtt, err := dnsClient.Exchange(m, svr)
	if err == nil && response != nil {
		statsMutex.Lock()
		dnsUsageStats[svr]++
		statsMutex.Unlock()
		if config.EnableMetrics {
			metricsRecorder.RecordUpstreamQuery(svr, "success", rtt)
		}
		logutil.Logger.Debugf("exchangeWithServer: end, server=%s, success", svr)
		return response.Answer, nil
	}
	logutil.Logger.Warnf("Exchange error using server %s: %v", svr, err)
	if config.EnableMetrics {
		metricsRecorder.RecordUpstreamQuery(svr, "error", rtt)
		metricsRecorder.RecordError("upstream_query_failed", "dns_exchange")
	}
	logutil.Logger.Debugf("exchangeWithServer: end, server=%s, error", svr)
	return nil, err
}
