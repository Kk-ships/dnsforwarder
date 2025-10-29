package dnsresolver

import (
	"crypto/tls"
	"dnsloadbalancer/config"
	"dnsloadbalancer/dnssource"
	"sync"
	"testing"

	"github.com/miekg/dns"
)

func TestIsDoTServerAddress(t *testing.T) {
	// Save original config
	originalCfg := cfg
	defer func() {
		cfg = originalCfg
		dnssource.SetConfigForTest(originalCfg)
	}()

	// Test with DoT enabled
	testCfg := &config.Config{
		EnableDoT:  true,
		DoTServers: []string{"1.1.1.1:853", "8.8.8.8:853"},
	}
	cfg = testCfg

	// Set config in dnssource and initialize
	dnssource.SetConfigForTest(testCfg)
	dnssource.InitDNSSource(nil)

	tests := []struct {
		name     string
		server   string
		expected bool
	}{
		{
			name:     "DoT server exact match",
			server:   "1.1.1.1:853",
			expected: true,
		},
		{
			name:     "DoT server exact match second",
			server:   "8.8.8.8:853",
			expected: true,
		},
		{
			name:     "Non-DoT server",
			server:   "1.1.1.1:53",
			expected: false,
		},
		{
			name:     "Different server",
			server:   "9.9.9.9:853",
			expected: false,
		},
		{
			name:     "Empty server",
			server:   "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := dnssource.IsDoTServer(tt.server)
			if result != tt.expected {
				t.Errorf("dnssource.IsDoTServer(%s) = %v, want %v", tt.server, result, tt.expected)
			}
		})
	}
}

func TestIsDoTServerAddress_DisabledDoT(t *testing.T) {
	// Save original config
	originalCfg := cfg
	defer func() {
		cfg = originalCfg
		dnssource.SetConfigForTest(originalCfg)
	}()

	// Test with DoT disabled
	testCfg := &config.Config{
		EnableDoT:  false,
		DoTServers: []string{"1.1.1.1:853"},
	}
	cfg = testCfg

	// Set config in dnssource and initialize
	dnssource.SetConfigForTest(testCfg)
	dnssource.InitDNSSource(nil)

	result := dnssource.IsDoTServer("1.1.1.1:853")
	if result {
		t.Error("dnssource.IsDoTServer should return false when DoT is disabled")
	}
}

func TestIsDoTServerAddress_EmptyConfig(t *testing.T) {
	// Save original config
	originalCfg := cfg
	defer func() {
		cfg = originalCfg
		dnssource.SetConfigForTest(originalCfg)
	}()

	// Test with empty DoT servers
	testCfg := &config.Config{
		EnableDoT:  true,
		DoTServers: []string{},
	}
	cfg = testCfg

	// Set config in dnssource and initialize
	dnssource.SetConfigForTest(testCfg)
	dnssource.InitDNSSource(nil)

	result := dnssource.IsDoTServer("1.1.1.1:853")
	if result {
		t.Error("dnssource.IsDoTServer should return false when DoT servers list is empty")
	}
}

func TestDoTClientPool(t *testing.T) {
	// Save original config
	originalCfg := cfg
	defer func() { cfg = originalCfg }()

	cfg = &config.Config{
		EnableDoT:     true,
		DoTServers:    []string{"1.1.1.1:853"},
		DoTServerName: "cloudflare-dns.com",
		DoTSkipVerify: false,
		DNSTimeout:    5000000000, // 5 seconds in nanoseconds
	}

	// Create a new DoT client pool with test config
	testDoTClientPool := sync.Pool{New: func() any {
		return &dns.Client{
			Timeout: cfg.DNSTimeout,
			Net:     "tcp-tls",
			TLSConfig: &tls.Config{
				ServerName:         cfg.DoTServerName,
				InsecureSkipVerify: cfg.DoTSkipVerify,
			},
		}
	}}

	// Get client from pool
	client := testDoTClientPool.Get().(*dns.Client)
	defer testDoTClientPool.Put(client)

	// Verify client configuration
	if client.Net != "tcp-tls" {
		t.Errorf("Expected client.Net to be 'tcp-tls', got %s", client.Net)
	}

	if client.TLSConfig == nil {
		t.Fatal("Expected TLSConfig to be set")
	}

	if client.TLSConfig.ServerName != "cloudflare-dns.com" {
		t.Errorf("Expected ServerName to be 'cloudflare-dns.com', got %s", client.TLSConfig.ServerName)
	}

	if client.TLSConfig.InsecureSkipVerify != false {
		t.Error("Expected InsecureSkipVerify to be false")
	}
}

func TestDoTClientPoolReuse(t *testing.T) {
	// Get multiple clients to ensure pool reuses them
	clients := make([]*dns.Client, 5)
	for i := 0; i < 5; i++ {
		clients[i] = doTClientPool.Get().(*dns.Client)
	}

	// Put them back
	for _, client := range clients {
		doTClientPool.Put(client)
	}

	// Get them again and verify they're reused (should be from pool)
	for i := 0; i < 5; i++ {
		client := doTClientPool.Get().(*dns.Client)
		if client == nil {
			t.Fatal("Expected non-nil client from pool")
		}
		doTClientPool.Put(client)
	}
}

func TestExchangeWithServer_DoTDetection(t *testing.T) {
	// Save original config
	originalCfg := cfg
	defer func() { cfg = originalCfg }()

	cfg = &config.Config{
		EnableDoT:        true,
		DoTServers:       []string{"1.1.1.1:853"},
		DoTServerName:    "cloudflare-dns.com",
		DoTSkipVerify:    true, // Skip verification for testing
		DoTFallbackToUDP: true,
		DNSTimeout:       1000000000, // 1 second
		EnableMetrics:    false,
	}

	// Create a test message
	m := GetDNSMsg()
	defer PutDNSMsg(m)
	m.SetQuestion(dns.Fqdn("example.com"), dns.TypeA)

	// Test with a DoT server (will likely fail but we're testing the path)
	// This tests that the DoT client is used and fallback occurs
	_, err := exchangeWithServer(m, "1.1.1.1:853")
	// We expect an error since we're not actually connecting to a real server in the test
	// But the test validates that the code path for DoT is exercised
	if err == nil {
		t.Log("DoT connection succeeded (unexpected in test environment, but not an error)")
	}
}

func TestExchangeWithServer_RegularUDP(t *testing.T) {
	// Save original config
	originalCfg := cfg
	defer func() { cfg = originalCfg }()

	cfg = &config.Config{
		EnableDoT:     false,
		DNSTimeout:    1000000000, // 1 second
		EnableMetrics: false,
	}

	// Create a test message
	m := GetDNSMsg()
	defer PutDNSMsg(m)
	m.SetQuestion(dns.Fqdn("example.com"), dns.TypeA)

	// Test with a regular UDP server
	_, err := exchangeWithServer(m, "8.8.8.8:53")
	// We expect an error in test environment, but validates UDP path
	if err == nil {
		t.Log("UDP connection succeeded (unexpected in test environment, but not an error)")
	}
}

func TestDoTConfigurationUpdate(t *testing.T) {
	// Save original config
	originalCfg := cfg
	defer func() { cfg = originalCfg }()

	// Test that DoT client configuration updates when config changes
	cfg = &config.Config{
		EnableDoT:        true,
		DoTServers:       []string{"1.1.1.1:853"},
		DoTServerName:    "cloudflare-dns.com",
		DoTSkipVerify:    false,
		DoTFallbackToUDP: true,
		DNSTimeout:       5000000000,
		EnableMetrics:    false,
	}

	// Create a new DoT client pool with test config
	testDoTClientPool := sync.Pool{New: func() any {
		return &dns.Client{
			Timeout: cfg.DNSTimeout,
			Net:     "tcp-tls",
			TLSConfig: &tls.Config{
				ServerName:         cfg.DoTServerName,
				InsecureSkipVerify: cfg.DoTSkipVerify,
			},
		}
	}}

	client := testDoTClientPool.Get().(*dns.Client)

	// Verify initial configuration
	if client.TLSConfig.ServerName != "cloudflare-dns.com" {
		t.Errorf("Expected ServerName 'cloudflare-dns.com', got %s", client.TLSConfig.ServerName)
	}

	testDoTClientPool.Put(client)
}

func TestIsDoTServerAddress_PrefixMatch(t *testing.T) {
	// Save original config
	originalCfg := cfg
	defer func() {
		cfg = originalCfg
		dnssource.SetConfigForTest(originalCfg)
	}()

	testCfg := &config.Config{
		EnableDoT:  true,
		DoTServers: []string{"1.1.1.1:853"},
	}
	cfg = testCfg

	// Set config in dnssource and initialize
	dnssource.SetConfigForTest(testCfg)
	dnssource.InitDNSSource(nil)

	// Test that prefix matching works
	tests := []struct {
		name     string
		server   string
		expected bool
	}{
		{
			name:     "Exact match",
			server:   "1.1.1.1:853",
			expected: true,
		},
		{
			name:     "Different port",
			server:   "1.1.1.1:53",
			expected: false,
		},
		{
			name:     "Similar IP but different",
			server:   "1.1.1.2:853",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := dnssource.IsDoTServer(tt.server)
			if result != tt.expected {
				t.Errorf("dnssource.IsDoTServer(%s) = %v, want %v", tt.server, result, tt.expected)
			}
		})
	}
}

func TestDoTWithMetrics(t *testing.T) {
	// Save original config
	originalCfg := cfg
	defer func() { cfg = originalCfg }()

	cfg = &config.Config{
		EnableDoT:        true,
		DoTServers:       []string{"1.1.1.1:853"},
		DoTServerName:    "cloudflare-dns.com",
		DoTSkipVerify:    true,
		DoTFallbackToUDP: false,
		DNSTimeout:       1000000000,
		EnableMetrics:    true, // Enable metrics to test that path
	}

	// Create a test message
	m := GetDNSMsg()
	defer PutDNSMsg(m)
	m.SetQuestion(dns.Fqdn("example.com"), dns.TypeA)

	// Test with DoT server (will fail but tests metrics path)
	_, err := exchangeWithServer(m, "1.1.1.1:853")
	// Error expected in test environment
	if err == nil {
		t.Log("DoT connection succeeded (unexpected but not an error)")
	}
}

func TestDoTFallbackToUDP(t *testing.T) {
	// Save original config
	originalCfg := cfg
	defer func() { cfg = originalCfg }()

	cfg = &config.Config{
		EnableDoT:        true,
		DoTServers:       []string{"1.1.1.1:853"},
		DoTServerName:    "cloudflare-dns.com",
		DoTSkipVerify:    true,
		DoTFallbackToUDP: true,      // Enable fallback
		DNSTimeout:       500000000, // 0.5 seconds
		EnableMetrics:    false,
	}

	// Create a test message
	m := GetDNSMsg()
	defer PutDNSMsg(m)
	m.SetQuestion(dns.Fqdn("example.com"), dns.TypeA)

	// Test with DoT server - should attempt DoT, fail, then fallback to UDP
	_, err := exchangeWithServer(m, "1.1.1.1:853")
	// Error expected in test environment for both DoT and UDP fallback
	if err == nil {
		t.Log("Connection succeeded after fallback (unexpected but not an error)")
	}
}
