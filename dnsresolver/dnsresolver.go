package dnsresolver

import (
	"crypto/tls"
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
	dnsUsageStats = make(map[string]*int64) // Changed to atomic counters
	statsMutex    sync.RWMutex              // Use RWMutex for better read performance
	dnsMsgPool    = sync.Pool{New: func() any { return new(dns.Msg) }}
	dnsClientPool = sync.Pool{New: func() any {
		return &dns.Client{
			Timeout: config.Get().DNSTimeout,
			Net:     "udp",
		}
	}}
	doTClientPool = sync.Pool{New: func() any {
		cfg := config.Get()
		return &dns.Client{
			Timeout: cfg.DNSTimeout,
			Net:     "tcp-tls",
			TLSConfig: &tls.Config{
				ServerName:         cfg.DoTServerName,
				InsecureSkipVerify: cfg.DoTSkipVerify,
			},
		}
	}}
	cfg = config.Get()
)

// GetDNSMsg gets a DNS message from the pool
func GetDNSMsg() *dns.Msg {
	return dnsMsgPool.Get().(*dns.Msg)
}

// PutDNSMsg returns a DNS message to the pool
func PutDNSMsg(msg *dns.Msg) {
	// Reset the message to ensure clean state
	*msg = dns.Msg{}
	dnsMsgPool.Put(msg)
}

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
				metric.GetFastMetricsInstance(), // Use fast metrics for all DNS operations
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
	m := GetDNSMsg()
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true
	return m
}

func ResolverForClient(domain string, qtype uint16, clientIP string) []dns.RR {
	m := prepareDNSQuery(domain, qtype)
	defer PutDNSMsg(m)
	privateServers, publicServers := dnssource.GetServersForClient(clientIP)
	return upstreamDNSQuery(privateServers, publicServers, m)
}

func ResolverForDomain(domain string, qtype uint16, clientIP string) []dns.RR {
	m := prepareDNSQuery(domain, qtype)
	defer PutDNSMsg(m)
	if svr, ok := domainrouting.GetRoutingTable()[domain]; ok {
		result := upstreamDNSQuery([]string{svr}, []string{}, m)
		return result
	}
	return ResolverForClient(domain, qtype, clientIP)

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
	// Determine if this is a DoT server
	isDoTServer := dnssource.IsDoTServer(svr)

	var dnsClient *dns.Client
	var rtt time.Duration
	var response *dns.Msg
	var err error

	if isDoTServer {
		// Use DoT client
		dnsClient = doTClientPool.Get().(*dns.Client)
		defer doTClientPool.Put(dnsClient)

		// Update timeout and TLS config if needed
		if dnsClient.Timeout != cfg.DNSTimeout {
			dnsClient.Timeout = cfg.DNSTimeout
		}
		if dnsClient.TLSConfig != nil {
			if dnsClient.TLSConfig.ServerName != cfg.DoTServerName {
				dnsClient.TLSConfig.ServerName = cfg.DoTServerName
			}
			if dnsClient.TLSConfig.InsecureSkipVerify != cfg.DoTSkipVerify {
				dnsClient.TLSConfig.InsecureSkipVerify = cfg.DoTSkipVerify
			}
		}

		response, rtt, err = dnsClient.Exchange(m, svr)

		// Fallback to UDP if DoT fails and fallback is enabled
		if err != nil && cfg.DoTFallbackToUDP {
			logutil.Logger.Debugf("DoT query failed for %s, falling back to UDP: %v", svr, err)
			dnsClient = dnsClientPool.Get().(*dns.Client)
			defer dnsClientPool.Put(dnsClient)
			if dnsClient.Timeout != cfg.DNSTimeout {
				dnsClient.Timeout = cfg.DNSTimeout
			}
			response, rtt, err = dnsClient.Exchange(m, svr)
		}
	} else {
		// Use regular UDP client
		dnsClient = dnsClientPool.Get().(*dns.Client)
		defer dnsClientPool.Put(dnsClient)

		// Update timeout from current config if needed
		if dnsClient.Timeout != cfg.DNSTimeout {
			dnsClient.Timeout = cfg.DNSTimeout
		}

		response, rtt, err = dnsClient.Exchange(m, svr)
	}

	if err == nil && response != nil {
		// Use atomic operations for stats - faster than mutex
		incrementServerUsage(svr)
		if cfg.EnableMetrics {
			protocol := "udp"
			if isDoTServer {
				protocol = "dot"
			}
			metric.GetFastMetricsInstance().FastRecordUpstreamQuery(svr, "success", rtt)
			metric.GetFastMetricsInstance().FastRecordError("upstream_protocol_"+protocol, "dns_exchange")
		}
		return response.Answer, nil
	}
	// Only log errors at debug level to reduce log noise in high-traffic scenarios
	logutil.Logger.Debugf("Exchange error using server %s: %v", svr, err)
	if cfg.EnableMetrics {
		metric.GetFastMetricsInstance().FastRecordUpstreamQuery(svr, "error", rtt)
		metric.GetFastMetricsInstance().FastRecordError("upstream_query_failed", "dns_exchange")
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
