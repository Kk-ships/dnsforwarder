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

var (
	CacheLastUpdated         time.Time
	CacheMutex               sync.RWMutex
	PrivateAndPublicFallback []string                    // Combined list of private and public servers
	PrivateServersSet        = make(map[string]struct{}) // Set of private DNS servers for fast lookup
	PublicServersSet         = make(map[string]struct{}) // Set of public DNS servers for fast lookup
	PublicServersFallback    []string                    // Copy of public servers for fallback
	allServersToTest         map[string]bool             // Map to hold all servers to test for reachability
	PrivateServersCache      []string                    // Cache for private servers which are healthy
	PublicServersCache       []string                    // Cache for public servers which are healthy
)

type metricsRecorderInterface interface {
	RecordError(string, string)
	SetUpstreamServersTotal(int)
	SetUpstreamServerReachable(string, bool)
	RecordUpstreamQuery(string, string, time.Duration)
}

func InitDNSSource(metricsRecorder metricsRecorderInterface) {
	logutil.Logger.Debug("InitDNSSource: start")
	addServersToSet(config.PrivateServers, PrivateServersSet)
	addServersToSet(config.PublicServers, PublicServersSet)
	PrivateAndPublicFallback = append(config.PrivateServers, config.PublicServers...)
	PublicServersFallback = config.PublicServers[:] // Create a copy
	logutil.Logger.Infof("Private servers: %v", config.PrivateServers)
	logutil.Logger.Infof("Public servers: %v", config.PublicServers)
	logutil.Logger.Infof("Combined servers: %v", PrivateAndPublicFallback)
	logutil.Logger.Debug("InitDNSSource: end")
}

func addServersToSet(servers []string, set map[string]struct{}) {
	logutil.Logger.Debugf("addServersToSet: start, servers=%v", servers)
	for _, s := range servers {
		if s != "" {
			set[s] = struct{}{}
		}
	}
	logutil.Logger.Debugf("addServersToSet: end, set=%v", set)
}

// GetDNSServers returns the list of DNS servers from environment or config which can be used for DNS queries.
// It combines private and public servers
func GetDNSServers() []string {
	logutil.Logger.Debug("GetDNSServers: start")
	servers := PrivateAndPublicFallback
	logutil.Logger.Debugf("GetDNSServers: returning %v", servers)
	return servers
}
func UpdateDNSServersCache(metricsRecorder metricsRecorderInterface,
	cacheTTL time.Duration,
	clientRoutingEnabled bool,
	privateServers, publicServers []string,
	dnsClient *dns.Client,
	dnsMsgPool *sync.Pool) {
	logutil.Logger.Debug("UpdateDNSServersCache: start")
	servers := GetDNSServers() // Get all configured DNS servers
	logutil.Logger.Debugf("UpdateDNSServersCache: servers=%v", servers)
	if len(servers) == 0 || (len(servers) == 1 && servers[0] == "") {
		metricsRecorder.RecordError("no_dns_servers", "config")
		logutil.Logger.Fatalf("No DNS servers found")
	}

	upstreamServerCount := len(servers)
	if config.EnableMetrics {
		metricsRecorder.SetUpstreamServersTotal(upstreamServerCount)
	}
	allServersToTest = make(map[string]bool)
	for _, server := range servers {
		if server != "" {
			allServersToTest[server] = true
		}
	}
	uniqueServers := make([]string, 0, len(allServersToTest))
	for server := range allServersToTest {
		uniqueServers = append(uniqueServers, server)
	}
	logutil.Logger.Debugf("UpdateDNSServersCache: uniqueServers=%v", uniqueServers)
	var (
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
			logutil.Logger.Debugf("UpdateDNSServersCache: goroutine start for server=%s", svr)
			defer wg.Done()
			defer func() { <-sem }()

			m := dnsMsgPool.Get().(*dns.Msg)
			m.SetQuestion(config.DefaultTestDomain+".", dns.TypeA)
			m.RecursionDesired = true

			_, rtt, err := dnsClient.Exchange(m, svr)
			dnsMsgPool.Put(m) // immediately return to pool

			if err != nil {
				logutil.Logger.Warnf("server %s is not reachable: %v", svr, err)
				if config.EnableMetrics {
					metricsRecorder.SetUpstreamServerReachable(svr, false)
					metricsRecorder.RecordUpstreamQuery(svr, "error", rtt)
					metricsRecorder.RecordError("upstream_unreachable", "health_check")
				}
				logutil.Logger.Debugf("UpdateDNSServersCache: goroutine end for server=%s (unreachable)", svr)
				return
			}

			if config.EnableMetrics {
				metricsRecorder.SetUpstreamServerReachable(svr, true)
				metricsRecorder.RecordUpstreamQuery(svr, "success", rtt)
			}
			mu.Lock()
			if IsPrivateServer(svr) {
				reachablePrivate = append(reachablePrivate, svr)
			} else if IsPublicServer(svr) {
				reachablePublic = append(reachablePublic, svr)
			}
			mu.Unlock()
			logutil.Logger.Debugf("UpdateDNSServersCache: goroutine end for server=%s (reachable)", svr)
		}(server)
	}

	wg.Wait()
	logutil.Logger.Debugf("UpdateDNSServersCache: all goroutines done, reachablePrivate=%v, reachablePublic=%v", reachablePrivate, reachablePublic)
	if len(reachablePrivate)+len(reachablePublic) == 0 {
		if config.EnableMetrics {
			metricsRecorder.RecordError("no_reachable_servers", "health_check")
		}
		logutil.Logger.Warn("No reachable DNS servers found")
	}
	CacheMutex.Lock()
	PrivateServersCache = reachablePrivate
	PublicServersCache = reachablePublic
	CacheLastUpdated = time.Now()
	CacheMutex.Unlock()
	logutil.Logger.Debug("UpdateDNSServersCache: end")
}
func ShouldUsePublicServers(clientIP string) bool {
	logutil.Logger.Debugf("ShouldUsePublicServers: start, clientIP=%s", clientIP)
	if !config.EnableClientRouting {
		logutil.Logger.Debug("ShouldUsePublicServers: client routing not enabled")
		return false
	}

	if _, exists := clientrouting.PublicOnlyClientsMap.Load(clientIP); exists {
		logutil.Logger.Debugf("ShouldUsePublicServers: clientIP %s found in PublicOnlyClientsMap", clientIP)
		return true
	}
	mac := util.GetMACFromARP(clientIP)
	if mac != "" {
		_, exists := clientrouting.PublicOnlyClientMACsMap.Load(mac)
		logutil.Logger.Debugf("ShouldUsePublicServers: MAC %s found in PublicOnlyClientMACsMap: %v", mac, exists)
		return exists
	}
	logutil.Logger.Debugf("ShouldUsePublicServers: clientIP %s not found in any map", clientIP)
	return false
}

func GetServersForClient(clientIP string, cacheMutex *sync.RWMutex) (privateServers []string, publicServers []string) {
	logutil.Logger.Debugf("GetServersForClient: start, clientIP=%s", clientIP)
	if ShouldUsePublicServers(clientIP) {
		cacheMutex.RLock()
		servers := PublicServersCache
		cacheMutex.RUnlock()
		if len(servers) > 0 {
			logutil.Logger.Debugf("GetServersForClient: using only public servers for clientIP=%s: %v", clientIP, servers)
			return []string{}, servers
		}
		logutil.Logger.Debugf("GetServersForClient: using only public fallback servers for clientIP=%s: %v", clientIP, PublicServersFallback)
		return []string{}, PublicServersFallback
	}
	cacheMutex.RLock()
	defer cacheMutex.RUnlock()
	if len(PrivateServersCache) == 0 && len(PublicServersCache) == 0 {
		logutil.Logger.Debugf("GetServersForClient: no cache, using fallback for clientIP=%s", clientIP)
		return []string{}, PrivateAndPublicFallback
	}
	logutil.Logger.Debugf("GetServersForClient: using private=%v, public=%v for clientIP=%s", PrivateServersCache, PublicServersCache, clientIP)
	return PrivateServersCache, PublicServersCache
}

func IsPrivateServer(server string) bool {
	exists := false
	if _, ok := PrivateServersSet[server]; ok {
		exists = true
	}
	logutil.Logger.Debugf("IsPrivateServer: server=%s, exists=%v", server, exists)
	return exists
}

func IsPublicServer(server string) bool {
	exists := false
	if _, ok := PublicServersSet[server]; ok {
		exists = true
	}
	logutil.Logger.Debugf("IsPublicServer: server=%s, exists=%v", server, exists)
	return exists
}
