package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// --- Reusable Helpers with Reduced Lock Contention ---

var (
	envDurationCache sync.Map // map[string]time.Duration
	envIntCache      sync.Map // map[string]int
	envStringCache   sync.Map // map[string]string
)

func getEnvDuration(key string, def time.Duration) time.Duration {
	if v, ok := envDurationCache.Load(key); ok {
		return v.(time.Duration)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			val = d
		}
	}
	envDurationCache.Store(key, val)
	return val
}

func getEnvInt(key string, def int) int {
	if v, ok := envIntCache.Load(key); ok {
		return v.(int)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		if i, err := strconv.Atoi(s); err == nil {
			val = i
		}
	}
	envIntCache.Store(key, val)
	return val
}

func getEnvString(key, def string) string {
	if v, ok := envStringCache.Load(key); ok {
		return v.(string)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		val = s
	}
	envStringCache.Store(key, val)
	return val
}

func getEnvStringSlice(key, def string) []string {
	if v := os.Getenv(key); v != "" {
		parts := strings.Split(v, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	return []string{def}
}

// --- Config ---

var (
	defaultCacheTTL     = getEnvDuration("CACHE_TTL", 10*time.Second)
	defaultDNSTimeout   = getEnvDuration("DNS_TIMEOUT", 5*time.Second)
	defaultWorkerCount  = getEnvInt("WORKER_COUNT", 5)
	defaultTestDomain   = getEnvString("TEST_DOMAIN", "google.com")
	defaultDNSPort      = getEnvString("DNS_PORT", ":53")
	defaultUDPSize      = getEnvInt("UDP_SIZE", 65535)
	defaultDNSStatslog  = getEnvDuration("DNS_STATSLOG", 5*time.Minute)
	defaultDNSServer    = getEnvString("DEFAULT_DNS_SERVER", "8.8.8.8:53")
)

var (
	reachableServersCache []string
	cacheLastUpdated      time.Time
	cacheMutex            sync.RWMutex
	cacheTTL              = defaultCacheTTL
	dnsClient             = &dns.Client{Timeout: defaultDNSTimeout}
)
var dnsMsgPool = sync.Pool{
	New: func() interface{} {
		return new(dns.Msg)
	},
}
var (
	dnsUsageStats = make(map[string]int)
	statsMutex    sync.Mutex
)

// --- Log Ring Buffer ---

type LogRingBuffer struct {
	entries []string
	max     int
	idx     int
	full    bool
	sync.Mutex
}

func NewLogRingBuffer(size int) *LogRingBuffer {
	return &LogRingBuffer{
		entries: make([]string, size),
		max:     size,
	}
}

func (l *LogRingBuffer) Add(entry string) {
	l.Lock()
	defer l.Unlock()
	l.entries[l.idx] = entry
	l.idx = (l.idx + 1) % l.max
	if l.idx == 0 {
		l.full = true
	}
}

func (l *LogRingBuffer) GetAll() []string {
	l.Lock()
	defer l.Unlock()
	if !l.full {
		return l.entries[:l.idx]
	}
	result := make([]string, l.max)
	copy(result, l.entries[l.idx:])
	copy(result[l.max-l.idx:], l.entries[:l.idx])
	return result
}

var logBuffer = NewLogRingBuffer(500)

func logWithBufferf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	logBuffer.Add(msg)
	log.Printf(format, v...)
}

func logWithBufferFatalf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	logBuffer.Add(msg)
	log.Fatalf(format, v...)
}

// --- DNS Server Selection ---

func getDNSServers() []string {
	return getEnvStringSlice("DNS_SERVERS", defaultDNSServer)
}

func updateDNSServersCache() {
	servers := getDNSServers()
	if len(servers) == 0 || (len(servers) == 1 && servers[0] == "") {
		logWithBufferFatalf("[ERROR] no DNS servers found")
	}
	m := dnsMsgPool.Get().(*dns.Msg)
	defer dnsMsgPool.Put(m)
	m.SetQuestion(dns.Fqdn(defaultTestDomain), dns.TypeA)
	m.RecursionDesired = true
	// Check servers in parallel
	reachableCh := make(chan string, len(servers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, defaultWorkerCount)
	for _, server := range servers {
		wg.Add(1)
		sem <- struct{}{}
		go func(svr string) {
			defer wg.Done()
			defer func() { <-sem }()
			_, _, err := dnsClient.Exchange(m.Copy(), svr)
			if err != nil {
				logWithBufferf("[WARNING] server %s is not reachable: %v", svr, err)
				return
			}
			reachableCh <- svr
		}(server)
	}

	wg.Wait()
	close(reachableCh)
	var reachable []string
	for svr := range reachableCh {
		reachable = append(reachable, svr)
	}
	if len(reachable) == 0 {
		logWithBufferFatalf("[ERROR] no reachable DNS servers found")
	}
	cacheMutex.Lock()
	reachableServersCache = reachable
	cacheLastUpdated = time.Now()
	cacheMutex.Unlock()
}

func getCachedDNSServers() []string {
	cacheMutex.RLock()
	if time.Since(cacheLastUpdated) <= cacheTTL && len(reachableServersCache) > 0 {
		servers := make([]string, len(reachableServersCache))
		copy(servers, reachableServersCache)
		cacheMutex.RUnlock()
		return servers
	}
	cacheMutex.RUnlock()

	// Update cache and try again
	updateDNSServersCache()

	cacheMutex.RLock()
	servers := make([]string, len(reachableServersCache))
	copy(servers, reachableServersCache)
	cacheMutex.RUnlock()
	return servers
}

// --- DNS Usage Logger ---

func startDNSUsageLogger() {
	ticker := time.NewTicker(defaultDNSStatslog)
	defer ticker.Stop()
	for range ticker.C {
		statsMutex.Lock()
		logWithBufferf("DNS Usage Stats: %+v", dnsUsageStats)
		statsMutex.Unlock()
	}
}

func startDNSServerCacheUpdater() {
	go startDNSUsageLogger()
	// Start cache updater in background
	go func() {
		ticker := time.NewTicker(cacheTTL)
		defer ticker.Stop()
		for range ticker.C {
			updateDNSServersCache()
		}
	}()
}

// --- DNS Resolver ---

func resolver(domain string, qtype uint16) []dns.RR {
	m := dnsMsgPool.Get().(*dns.Msg)
	defer dnsMsgPool.Put(m)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true

	servers := getCachedDNSServers()
	var response *dns.Msg
	var err error

	for _, svr := range servers {
		response, _, err = dnsClient.Exchange(m, svr)
		if err == nil && response != nil {
			statsMutex.Lock()
			dnsUsageStats[svr]++
			statsMutex.Unlock()
			return response.Answer
		}
		logWithBufferf("[WARNING] exchange error using server %s: %v", svr, err)
	}
	logWithBufferFatalf("[ERROR] all DNS exchanges failed")
	return nil
}

// --- DNS Handler ---

type dnsHandler struct{}

func (h *dnsHandler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true

	if len(r.Question) > 0 {
		q := r.Question[0]
		answers := resolver(q.Name, q.Qtype)
		if answers != nil {
			msg.Answer = answers
		}
	}

	if err := w.WriteMsg(msg); err != nil {
		logWithBufferf("[ERROR] Failed to write DNS response: %v", err)
	}
}

// --- Server Startup ---

func StartDNSServer() {
	updateDNSServersCache()

	handler := new(dnsHandler)
	server := &dns.Server{
		Addr:      defaultDNSPort,
		Net:       "udp",
		Handler:   handler,
		UDPSize:   defaultUDPSize,
		ReusePort: true,
	}

	logWithBufferf("Starting DNS server on port 53")

	startDNSServerCacheUpdater()

	err := server.ListenAndServe()
	if err != nil {
		logWithBufferFatalf("Failed to start server: %s\n", err.Error())
	}
}

func main() {
	StartDNSServer()
}

