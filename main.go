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

var (
	defaultCacheTTL = func() time.Duration {
		if v := os.Getenv("CACHE_TTL"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				return d
			}
		}
		return 10 * time.Second
	}()

	defaultDNSTimeout = func() time.Duration {
		if v := os.Getenv("DNS_TIMEOUT"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				return d
			}
		}
		return 5 * time.Second
	}()

	defaultWorkerCount = func() int {
		if v := os.Getenv("WORKER_COUNT"); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				return i
			}
		}
		return 5
	}()

	defaultTestDomain = func() string {
		if v := os.Getenv("TEST_DOMAIN"); v != "" {
			return v
		}
		return "google.com"
	}()

	defaultDNSPort = func() string {
		if v := os.Getenv("DNS_PORT"); v != "" {
			return v
		}
		return ":53"
	}()

	defaultUDPSize = func() int {
		if v := os.Getenv("UDP_SIZE"); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				return i
			}
		}
		return 65535
	}()

	defaultDNSStatslog = func() time.Duration {
		if v := os.Getenv("DNS_STATSLOG"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				return d
			}
		}
		return 5 * time.Minute
	}()

	defaultDNSServer = func() string {
		if v := os.Getenv("DEFAULT_DNS_SERVER"); v != "" {
			return v
		}
		return "8.8.8.8:53"
	}()
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

// Log ring buffer to keep last 500 logs
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
	msg := format
	if len(v) > 0 {
		msg = sprintf(format, v...)
	}
	logBuffer.Add(msg)
	log.Printf(format, v...)
}

func logWithBufferFatalf(format string, v ...interface{}) {
	msg := format
	if len(v) > 0 {
		msg = sprintf(format, v...)
	}
	logBuffer.Add(msg)
	log.Fatalf(format, v...)
}

func sprintf(format string, v ...interface{}) string {
	return fmt.Sprintf(format, v...)
}

func updateDNSServersCache() {
	// Don't lock for the entire update process
	env := os.Getenv("DNS_SERVERS")
	var servers []string
	if env == "" {
		servers = []string{defaultDNSServer}
	} else {
		servers = strings.Split(env, ",")
		for i, s := range servers {
			servers[i] = strings.TrimSpace(s)
		}
	}
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

func StartDNSServer() {
	// Initialize cache on startup
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
