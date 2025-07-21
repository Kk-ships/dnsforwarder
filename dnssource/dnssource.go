package dnssource

import (
	"dnsloadbalancer/clientrouting"
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/util"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// GetDNSServers returns the list of DNS servers from environment or config
func GetDNSServers() []string {
	return util.GetEnvStringSlice("DNS_SERVERS", config.DefaultDNSServer)
}

var (
	ReachableServersCache []string
	CacheLastUpdated      time.Time
	CacheMutex            sync.RWMutex
)

func UpdateDNSServersCache(metricsRecorder interface {
	RecordError(string, string)
	SetUpstreamServersTotal(int)
	SetUpstreamServerReachable(string, bool)
	RecordUpstreamQuery(string, string, time.Duration)
},
	cacheTTL time.Duration,
	clientRoutingEnabled bool,
	privateServers, publicServers []string,
	dnsClient *dns.Client,
	dnsMsgPool *sync.Pool) {

	servers := GetDNSServers()
	if len(servers) == 0 || (len(servers) == 1 && servers[0] == "") {
		metricsRecorder.RecordError("no_dns_servers", "config")
		logutil.LogWithBufferFatalf("[ERROR] no DNS servers found")
	}

	if config.EnableMetrics {
		metricsRecorder.SetUpstreamServersTotal(len(servers))
	}

	type result struct {
		server string
		ok     bool
	}

	allServersToTest := make([]string, 0)
	allServersToTest = append(allServersToTest, servers...)
	if clientRoutingEnabled {
		allServersToTest = append(allServersToTest, privateServers...)
		allServersToTest = append(allServersToTest, publicServers...)
	}

	serverSet := make(map[string]bool)
	uniqueServers := make([]string, 0)
	for _, server := range allServersToTest {
		if server != "" && !serverSet[server] {
			serverSet[server] = true
			uniqueServers = append(uniqueServers, server)
		}
	}

	m := dnsMsgPool.Get().(*dns.Msg)
	defer dnsMsgPool.Put(m)
	m.SetQuestion(config.DefaultTestDomain+".", dns.TypeA)
	m.RecursionDesired = true

	reachableCh := make(chan result, len(uniqueServers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, config.DefaultWorkerCount)

	for _, server := range uniqueServers {
		wg.Add(1)
		sem <- struct{}{}
		go func(svr string) {
			defer wg.Done()
			defer func() { <-sem }()

			start := time.Now()
			_, _, err := dnsClient.Exchange(m.Copy(), svr)
			duration := time.Since(start)

			if err != nil {
				logutil.LogWithBufferf("[WARNING] server %s is not reachable: %v", svr, err)
				if config.EnableMetrics {
					metricsRecorder.SetUpstreamServerReachable(svr, false)
					metricsRecorder.RecordUpstreamQuery(svr, "error", duration)
					metricsRecorder.RecordError("upstream_unreachable", "health_check")
				}
				reachableCh <- result{server: svr, ok: false}
				return
			}

			if config.EnableMetrics {
				metricsRecorder.SetUpstreamServerReachable(svr, true)
				metricsRecorder.RecordUpstreamQuery(svr, "success", duration)
			}
			reachableCh <- result{server: svr, ok: true}
		}(server)
	}

	wg.Wait()
	close(reachableCh)
	var reachable []string
	var reachablePrivate []string
	var reachablePublic []string

	for res := range reachableCh {
		if res.ok {
			reachable = append(reachable, res.server)
			if clientRoutingEnabled {
				if clientrouting.IsPrivateServer(res.server) {
					reachablePrivate = append(reachablePrivate, res.server)
				} else if clientrouting.IsPublicServer(res.server) {
					reachablePublic = append(reachablePublic, res.server)
				}
			}
		}
	}

	if len(reachable) == 0 {
		if config.EnableMetrics {
			metricsRecorder.RecordError("no_reachable_servers", "health_check")
		}
		logutil.LogWithBufferFatalf("[ERROR] no reachable DNS servers found")
	}

	CacheMutex.Lock()
	ReachableServersCache = reachable
	if clientRoutingEnabled {
		clientrouting.PrivateServersCache = reachablePrivate
		clientrouting.PublicServersCache = reachablePublic
	}
	CacheLastUpdated = time.Now()
	CacheMutex.Unlock()
}
