package dnsresolver

import (
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestConcurrentServerUsage(t *testing.T) {
	// Reset stats
	statsMutex.Lock()
	dnsUsageStats = make(map[string]*int64)
	statsMutex.Unlock()

	server := "test-server"
	numGoroutines := 100
	incrementsPerGoroutine := 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Start concurrent increments
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < incrementsPerGoroutine; j++ {
				incrementServerUsage(server)
			}
		}()
	}

	wg.Wait()

	stats := GetServerUsageStats()
	expected := int64(numGoroutines * incrementsPerGoroutine)

	if stats[server] != expected {
		t.Errorf("Expected %d, got %d", expected, stats[server])
	}
}

func TestDNSClientPooling(t *testing.T) {
	// Get client from pool
	client1 := dnsClientPool.Get().(*dns.Client)

	// Modify timeout
	client1.Timeout = time.Second * 10

	// Return to pool
	dnsClientPool.Put(client1)

	// Get another client
	client2 := dnsClientPool.Get().(*dns.Client)

	// Should be the same instance (pooled)
	if client1 != client2 {
		t.Log("Different client instances - normal if pool has multiple clients")
	}

	// Return to pool
	dnsClientPool.Put(client2)
}

func TestDNSMsgPooling(t *testing.T) {
	domain := "example.com"
	qtype := uint16(dns.TypeA)

	// Get message from pool
	msg := prepareDNSQuery(domain, qtype)

	// Verify it's properly initialized
	if len(msg.Question) != 1 {
		t.Errorf("Expected 1 question, got %d", len(msg.Question))
	}

	if msg.Question[0].Name != dns.Fqdn(domain) {
		t.Errorf("Expected %s, got %s", dns.Fqdn(domain), msg.Question[0].Name)
	}

	if msg.Question[0].Qtype != qtype {
		t.Errorf("Expected %d, got %d", qtype, msg.Question[0].Qtype)
	}

	if !msg.RecursionDesired {
		t.Error("RecursionDesired should be true")
	}

	// Return to pool
	dnsMsgPool.Put(msg)
}

func BenchmarkIncrementServerUsage(b *testing.B) {
	server := "benchmark-server"

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			incrementServerUsage(server)
		}
	})
}

func BenchmarkDNSClientPooling(b *testing.B) {
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			client := dnsClientPool.Get().(*dns.Client)
			dnsClientPool.Put(client)
		}
	})
}

func BenchmarkDNSMsgPooling(b *testing.B) {
	domain := "example.com"
	qtype := uint16(dns.TypeA)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			msg := prepareDNSQuery(domain, qtype)
			dnsMsgPool.Put(msg)
		}
	})
}
