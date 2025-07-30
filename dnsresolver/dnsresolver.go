package dnsresolver

import (
	"dnsloadbalancer/config"
	"dnsloadbalancer/dnssource"
	"dnsloadbalancer/domainrouting"
	"dnsloadbalancer/logutil"
	"sync/atomic"
	"time"

	"sync"

	"dnsloadbalancer/metric"

	"github.com/miekg/dns"
)

var (
	fastMetricsRecorder = metric.FastMetricsInstance
	dnsUsageStats       = make(map[string]*int64) // Changed to atomic counters
	statsMutex          sync.RWMutex              // Use RWMutex for better read performance
	dnsMsgPool          = sync.Pool{New: func() interface{} { return new(dns.Msg) }}
	dnsClientPool       = sync.Pool{New: func() interface{} {
		return &dns.Client{
			Timeout: config.Get().DNSTimeout,
			Net:     "udp",
		}
	}}
	cfg = config.Get()
)

func UpdateDNSServersCache() {
	cacheRefresh := cfg.CacheRefresh
	dnsClient := dnsClientPool.Get().(*dns.Client)
	defer dnsClientPool.Put(dnsClient)
	if dnsClient.Timeout != cfg.DNSTimeout {
		dnsClient.Timeout = cfg.DNSTimeout
	}

	ticker := time.NewTicker(cacheRefresh)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			dnssource.UpdateDNSServersCache(
				fastMetricsRecorder, // Use fast metrics for all DNS operations
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
	// Reset the message to ensure clean state
	*m = dns.Msg{}
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
	if svr, ok := domainrouting.GetRoutingTable()[domain]; ok {
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
	// Get pooled DNS client for better performance
	dnsClient := dnsClientPool.Get().(*dns.Client)
	defer dnsClientPool.Put(dnsClient)

	// Update timeout from current config if needed
	if dnsClient.Timeout != cfg.DNSTimeout {
		dnsClient.Timeout = cfg.DNSTimeout
	}

	response, rtt, err := dnsClient.Exchange(m, svr)
	if err == nil && response != nil {
		// Use atomic operations for stats - faster than mutex
		incrementServerUsage(svr)
		if cfg.EnableMetrics {
			fastMetricsRecorder.FastRecordUpstreamQuery(svr, "success", rtt)
		}
		return response.Answer, nil
	}
	// Only log errors at debug level to reduce log noise in high-traffic scenarios
	logutil.Logger.Debugf("Exchange error using server %s: %v", svr, err)
	if cfg.EnableMetrics {
		fastMetricsRecorder.FastRecordUpstreamQuery(svr, "error", rtt)
		fastMetricsRecorder.FastRecordError("upstream_query_failed", "dns_exchange")
	}
	return nil, err
}

// incrementServerUsage atomically increments the usage counter for a server
func incrementServerUsage(server string) {
	statsMutex.RLock()
	counter, exists := dnsUsageStats[server]
	statsMutex.RUnlock()
	if !exists {
		statsMutex.Lock()
		// Double-check after acquiring write lock
		if counter, exists = dnsUsageStats[server]; !exists {
			counter = new(int64)
			dnsUsageStats[server] = counter
		}
		statsMutex.Unlock()
	}
	atomic.AddInt64(counter, 1)
}

// GetServerUsageStats returns a snapshot of server usage statistics
func GetServerUsageStats() map[string]int64 {
	statsMutex.RLock()
	defer statsMutex.RUnlock()
	stats := make(map[string]int64, len(dnsUsageStats))
	for server, counter := range dnsUsageStats {
		stats[server] = atomic.LoadInt64(counter)
	}
	return stats
}
