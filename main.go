package main

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/miekg/dns"
)

func getDNSServers() []string {
	env := os.Getenv("DNS_SERVERS")
	log.Printf("DNS_SERVERS: %s", env)
	if env == "" {
		return []string{"8.8.8.8:53"}
	}
	servers := strings.Split(env, ",")
	for i, s := range servers {
		servers[i] = strings.TrimSpace(s)
	}
	log.Printf("Using servers : %v", servers)
	if len(servers) == 0 {
		log.Fatalf("[ERROR] no DNS servers found")
	}
	if len(servers) == 1 && servers[0] == "" {
		log.Fatalf("[ERROR] no DNS servers found")
	}
	if len(servers) > 1 {
		log.Printf("[INFO] using multiple DNS servers: %v", servers)
	} else {
		log.Printf("[INFO] using single DNS server: %s", servers[0])
	}
	// Check if the first server is reachable
	c := &dns.Client{Timeout: 5 * time.Second}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn("google.com"), dns.TypeA)
	m.RecursionDesired = true
	_, _, err := c.Exchange(m, servers[0])
	if err != nil {
		log.Printf("[WARNING] server %s is not reachable: %v", servers[0], err)
		servers = servers[1:] // Remove the first server from the list
		if len(servers) == 0 {
			log.Fatalf("[ERROR] no reachable DNS servers found")
		}
		log.Printf("[INFO] using remaining DNS servers: %v", servers)
	}
    if err == nil {
        log.Printf("[INFO] server %s is reachable", servers[0])
        return servers
    }
	// Check if the remaining servers are reachable
	var reachableServers []string
	for _, server := range servers {
		_, _, err := c.Exchange(m, server)
		if err != nil {
			log.Printf("[WARNING] server %s is not reachable: %v", server, err)
			continue
		}
		log.Printf("[INFO] server %s is reachable", server)
		reachableServers = append(reachableServers, server)
	}
	if len(reachableServers) == 0 {
	    log.Fatalf("[ERROR] no reachable DNS servers found")
    }
	return reachableServers
}

func resolver(domain string, qtype uint16) []dns.RR {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true
	servers := getDNSServers()
	c := &dns.Client{Timeout: 1 * time.Second}
	var response *dns.Msg
	var err error
	for _, svr := range servers {
		response, _, err = c.Exchange(m, svr)
		if err != nil {
			log.Printf("[WARNING] exchange error using server %s: %v", svr, err)
			continue
		}
		if response == nil {
			log.Printf("[WARNING] no response from server %s", svr)
			continue
		}
		// Valid response obtained, break out of the loop
		log.Printf("[INFO] response from server %s", svr)
		break
	}

	if response == nil {
		log.Fatalf("[ERROR] all DNS exchanges failed")
		return nil
	}

	return response.Answer
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

	err := server.ListenAndServe()
	if err != nil {
		log.Printf("Failed to start server: %s\n", err.Error())
	}
}

func main() {
	StartDNSServer()
}
