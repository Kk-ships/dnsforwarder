package main

import (
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	defaultCacheTTL    = 10 * time.Second
	defaultDNSTimeout  = 5 * time.Second
	defaultWorkerCount = 5
	defaultTestDomain  = "google.com"
	defaultDNSPort     = ":53"
	defaultUDPSize     = 65535
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

func updateDNSServersCache() {
	// Don't lock for the entire update process
	env := os.Getenv("DNS_SERVERS")
	log.Printf("DNS_SERVERS: %s", env)
	var servers []string
	if env == "" {
		servers = []string{"8.8.8.8:53"}
	} else {
		servers = strings.Split(env, ",")
		for i, s := range servers {
			servers[i] = strings.TrimSpace(s)
		}
	}
	if len(servers) == 0 || (len(servers) == 1 && servers[0] == "") {
		log.Fatalf("[ERROR] no DNS servers found")
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
				log.Printf("[WARNING] server %s is not reachable: %v", svr, err)
				return
			}
			log.Printf("[INFO] server %s is reachable", svr)
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
		log.Fatalf("[ERROR] no reachable DNS servers found")
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

func startDNSServerCacheUpdater() {
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
			log.Printf("[INFO] response from server %s", svr)
			return response.Answer
		}
		log.Printf("[WARNING] exchange error using server %s: %v", svr, err)
	}

	log.Fatalf("[ERROR] all DNS exchanges failed")
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
		log.Printf("[ERROR] Failed to write DNS response: %v", err)
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

	log.Printf("Starting DNS server on port 53")

	startDNSServerCacheUpdater()

	err := server.ListenAndServe()
	if err != nil {
		log.Fatalf("Failed to start server: %s\n", err.Error())
	}
}

func main() {
	StartDNSServer()
}
