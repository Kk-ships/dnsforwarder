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

type metricsRecorderInterface interface {
	RecordError(string, string)
	SetUpstreamServersTotal(int)
	SetUpstreamServerReachable(string, bool)
	RecordUpstreamQuery(string, string, time.Duration)
}

func UpdateDNSServersCache(metricsRecorder metricsRecorderInterface,
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

	upstreamServerCount := len(servers)
	if clientRoutingEnabled {
		upstreamServerCount += len(privateServers) + len(publicServers)
	}

	if config.EnableMetrics {
		metricsRecorder.SetUpstreamServersTotal(upstreamServerCount)
	}

	allServersToTest := make(map[string]bool)
	for _, server := range servers {
		if server != "" {
			allServersToTest[server] = true
		}
	}
	if clientRoutingEnabled {
		for _, server := range privateServers {
			if server != "" {
				allServersToTest[server] = true
			}
		}
		for _, server := range publicServers {
			if server != "" {
				allServersToTest[server] = true
			}
		}
	}

	uniqueServers := make([]string, 0, len(allServersToTest))
	for server := range allServersToTest {
		uniqueServers = append(uniqueServers, server)
	}

	var (
		reachable        = make([]string, 0, len(uniqueServers))  // preallocate
		reachablePrivate = make([]string, 0, len(privateServers)) // preallocate
		reachablePublic  = make([]string, 0, len(publicServers))  // preallocate
		mu               sync.Mutex
		wg               sync.WaitGroup
		sem              = make(chan struct{}, config.DefaultWorkerCount)
	)

	for _, server := range uniqueServers {
		wg.Add(1)
		sem <- struct{}{}
		go func(svr string) {
			defer wg.Done()
			defer func() { <-sem }()

			m := dnsMsgPool.Get().(*dns.Msg)
			m.SetQuestion(config.DefaultTestDomain+".", dns.TypeA)
			m.RecursionDesired = true

			_, rtt, err := dnsClient.Exchange(m, svr)
			dnsMsgPool.Put(m) // immediately return to pool

			if err != nil {
				logutil.LogWithBufferf("[WARNING] server %s is not reachable: %v", svr, err)
				if config.EnableMetrics {
					metricsRecorder.SetUpstreamServerReachable(svr, false)
					metricsRecorder.RecordUpstreamQuery(svr, "error", rtt)
					metricsRecorder.RecordError("upstream_unreachable", "health_check")
				}
				return
			}

			if config.EnableMetrics {
				metricsRecorder.SetUpstreamServerReachable(svr, true)
				metricsRecorder.RecordUpstreamQuery(svr, "success", rtt)
			}

			mu.Lock()
			reachable = append(reachable, svr)
			if clientRoutingEnabled {
				if clientrouting.IsPrivateServer(svr) {
					reachablePrivate = append(reachablePrivate, svr)
				} else if clientrouting.IsPublicServer(svr) {
					reachablePublic = append(reachablePublic, svr)
				}
			}
			mu.Unlock()
		}(server)
	}

	wg.Wait()
	close(sem)

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
