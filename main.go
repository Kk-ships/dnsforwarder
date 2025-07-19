package main

import (
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

var (
	reachableServersCache []string
	cacheLastUpdated      time.Time
	cacheMutex            = &sync.Mutex{}
	cacheTTL              = 10 * time.Second
)
var dnsClient = &dns.Client{Timeout: 5 * time.Second}
var dnsMsgPool = sync.Pool{
	New: func() interface{} {
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn("google.com"), dns.TypeA)
		m.RecursionDesired = true
		return m
	},
}

func updateDNSServersCache() {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

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
	m.SetQuestion(dns.Fqdn("google.com"), dns.TypeA)
	m.RecursionDesired = true
	// check if servers are reachable in parallel not to block the main thread
	var reachable []string
	var wg sync.WaitGroup
	reachableCh := make(chan string, len(servers))
	workerCount := 5
	sem := make(chan struct{}, workerCount)
	for _, server := range servers {
		wg.Add(1)
		sem <- struct{}{}
		go func(svr string) {
			defer wg.Done()
			defer func() { <-sem }()
			_, _, err := dnsClient.Exchange(m, svr)
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
	for svr := range reachableCh {
		reachable = append(reachable, svr)
	}
	if len(reachable) == 0 {
		log.Fatalf("[ERROR] no reachable DNS servers found")
	}
	reachableServersCache = reachable
	cacheLastUpdated = time.Now()
}

func getCachedDNSServers() []string {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()
	if time.Since(cacheLastUpdated) > cacheTTL || len(reachableServersCache) == 0 {
		updateDNSServersCache()
	}
	return append([]string(nil), reachableServersCache...)
}

func startDNSServerCacheUpdater() {
	go func() {
		for {
			updateDNSServersCache()
			time.Sleep(cacheTTL)
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

	for _, question := range r.Question {
		answers := resolver(question.Name, question.Qtype)
		msg.Answer = append(msg.Answer, answers...)
	}

	w.WriteMsg(msg)
}

func StartDNSServer() {
	handler := new(dnsHandler)
	server := &dns.Server{
		Addr:      ":53",
		Net:       "udp",
		Handler:   handler,
		UDPSize:   65535,
		ReusePort: true,
	}

	log.Printf("Starting DNS server on port 53")
	startDNSServerCacheUpdater()

	err := server.ListenAndServe()
	if err != nil {
		log.Printf("Failed to start server: %s\n", err.Error())
	}
}

func main() {
	StartDNSServer()
}
