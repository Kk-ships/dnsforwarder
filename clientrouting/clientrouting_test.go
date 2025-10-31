package clientrouting

import (
	"dnsloadbalancer/config"
	"dnsloadbalancer/util"
	"sync"
	"testing"

	"github.com/patrickmn/go-cache"
)

// Helper function to reset routing maps for testing
func resetRoutingMaps() {
	PublicOnlyClientsMap = sync.Map{}
	PublicOnlyClientMACsMap = sync.Map{}
	PublicOnlyClientOUIMap = sync.Map{}
	macCache = cache.New(macCacheTTL, 2*macCacheTTL)
}

func TestStoreClientsToMap(t *testing.T) {
	tests := []struct {
		name     string
		clients  []string
		expected []string
	}{
		{
			name:     "single IP",
			clients:  []string{"192.168.1.100"},
			expected: []string{"192.168.1.100"},
		},
		{
			name:     "multiple IPs",
			clients:  []string{"192.168.1.100", "10.0.0.1", "172.16.0.1"},
			expected: []string{"192.168.1.100", "10.0.0.1", "172.16.0.1"},
		},
		{
			name:     "IPs with whitespace",
			clients:  []string{" 192.168.1.100 ", "  10.0.0.1"},
			expected: []string{"192.168.1.100", "10.0.0.1"},
		},
		{
			name:     "empty strings filtered",
			clients:  []string{"192.168.1.100", "", "10.0.0.1"},
			expected: []string{"192.168.1.100", "10.0.0.1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &sync.Map{}
			storeClientsToMap(tt.clients, m, "IP")

			// Check all expected values are stored
			for _, expected := range tt.expected {
				if _, exists := m.Load(expected); !exists {
					t.Errorf("Expected IP %s not found in map", expected)
				}
			}
		})
	}
}

func TestStoreMACsToMap(t *testing.T) {
	tests := []struct {
		name     string
		macs     []string
		expected []string
	}{
		{
			name:     "single MAC",
			macs:     []string{"00:11:22:33:44:55"},
			expected: []string{"00:11:22:33:44:55"},
		},
		{
			name:     "MAC with different formats",
			macs:     []string{"00:11:22:33:44:55", "AA-BB-CC-DD-EE-FF", "12.34.56.78.9a.bc"},
			expected: []string{"00:11:22:33:44:55", "aa:bb:cc:dd:ee:ff", "12:34:56:78:9a:bc"},
		},
		{
			name:     "mixed case normalization",
			macs:     []string{"AA:BB:CC:DD:EE:FF"},
			expected: []string{"aa:bb:cc:dd:ee:ff"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &sync.Map{}
			storeMACsToMap(tt.macs, m)

			// Check all expected values are stored
			for _, expected := range tt.expected {
				if _, exists := m.Load(expected); !exists {
					t.Errorf("Expected MAC %s not found in map", expected)
				}
			}
		})
	}
}

func TestStoreOUIsToMap(t *testing.T) {
	tests := []struct {
		name     string
		ouis     []string
		expected []string
	}{
		{
			name:     "single OUI",
			ouis:     []string{"68:57:2d"},
			expected: []string{"68:57:2d"},
		},
		{
			name:     "multiple OUIs",
			ouis:     []string{"68:57:2d", "10:5a:17", "d8:1f:12"},
			expected: []string{"68:57:2d", "10:5a:17", "d8:1f:12"},
		},
		{
			name:     "OUI with different formats",
			ouis:     []string{"68:57:2d", "10-5A-17", "D8.1F.12"},
			expected: []string{"68:57:2d", "10:5a:17", "d8:1f:12"},
		},
		{
			name:     "partial OUIs",
			ouis:     []string{"68:57", "10"},
			expected: []string{"68:57", "10"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &sync.Map{}
			storeOUIsToMap(tt.ouis, m)

			// Check all expected values are stored
			for _, expected := range tt.expected {
				if _, exists := m.Load(expected); !exists {
					t.Errorf("Expected OUI %s not found in map", expected)
				}
			}
		})
	}
}

func TestGetMACWithCache(t *testing.T) {
	resetRoutingMaps()

	tests := []struct {
		name      string
		clientIP  string
		cacheMAC  string
		cacheSet  bool
		expectHit bool
	}{
		{
			name:      "cache hit",
			clientIP:  "192.168.1.100",
			cacheMAC:  "00:11:22:33:44:55",
			cacheSet:  true,
			expectHit: true,
		},
		{
			name:      "cache miss",
			clientIP:  "192.168.1.200",
			cacheMAC:  "",
			cacheSet:  false,
			expectHit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.cacheSet {
				macCache.Set(tt.clientIP, tt.cacheMAC, macCacheTTL)
			}

			result := getMACWithCache(tt.clientIP)

			if tt.expectHit && result != tt.cacheMAC {
				t.Errorf("Expected cached MAC %s, got %s", tt.cacheMAC, result)
			}

			// Verify the result is now cached
			if cachedMAC, found := macCache.Get(tt.clientIP); !found {
				t.Error("MAC should be cached after retrieval")
			} else if tt.expectHit && cachedMAC != tt.cacheMAC {
				t.Errorf("Cached MAC mismatch: expected %s, got %s", tt.cacheMAC, cachedMAC)
			}
		})
	}
}

func TestShouldUsePublicServers_IPBased(t *testing.T) {
	resetRoutingMaps()

	// Manually set up test data instead of relying on env vars
	cfg = &config.Config{EnableClientRouting: true}

	// Add test IPs directly to the map
	PublicOnlyClientsMap.Store("192.168.1.100", true)
	PublicOnlyClientsMap.Store("10.0.0.50", true)

	tests := []struct {
		name     string
		clientIP string
		expected bool
	}{
		{
			name:     "IP in public-only list",
			clientIP: "192.168.1.100",
			expected: true,
		},
		{
			name:     "another IP in public-only list",
			clientIP: "10.0.0.50",
			expected: true,
		},
		{
			name:     "IP not in public-only list",
			clientIP: "192.168.1.200",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ShouldUsePublicServers(tt.clientIP)
			if result != tt.expected {
				t.Errorf("ShouldUsePublicServers(%s) = %v, want %v", tt.clientIP, result, tt.expected)
			}
		})
	}
}

func TestShouldUsePublicServers_MACBased(t *testing.T) {
	resetRoutingMaps()

	// Manually set up test data
	testMAC := "00:11:22:33:44:55"
	PublicOnlyClientMACsMap.Store(testMAC, true)

	// Create a mock cache entry
	testIP := "192.168.1.100"
	macCache.Set(testIP, testMAC, macCacheTTL)

	cfg = &config.Config{EnableClientRouting: true}

	result := ShouldUsePublicServers(testIP)
	if !result {
		t.Errorf("ShouldUsePublicServers(%s) with MAC %s should return true", testIP, testMAC)
	}

	// Test with IP that has no MAC
	testIP2 := "192.168.1.200"
	macCache.Set(testIP2, "", macCacheTTL)

	result2 := ShouldUsePublicServers(testIP2)
	if result2 {
		t.Errorf("ShouldUsePublicServers(%s) with no MAC should return false", testIP2)
	}
}

func TestShouldUsePublicServers_OUIBased(t *testing.T) {
	resetRoutingMaps()

	// Set up test OUI prefixes (TUYA devices)
	tuyaOUIs := []string{"68:57:2d", "10:5a:17", "d8:1f:12"}
	for _, oui := range tuyaOUIs {
		PublicOnlyClientOUIMap.Store(oui, true)
	}

	cfg = &config.Config{EnableClientRouting: true}

	tests := []struct {
		name     string
		clientIP string
		mac      string
		expected bool
		desc     string
	}{
		{
			name:     "TUYA device OUI 68:57:2d",
			clientIP: "192.168.1.100",
			mac:      "68:57:2d:aa:bb:cc",
			expected: true,
			desc:     "Should match TUYA OUI prefix",
		},
		{
			name:     "TUYA device OUI 10:5a:17",
			clientIP: "192.168.1.101",
			mac:      "10:5a:17:11:22:33",
			expected: true,
			desc:     "Should match another TUYA OUI prefix",
		},
		{
			name:     "TUYA device OUI d8:1f:12",
			clientIP: "192.168.1.102",
			mac:      "d8:1f:12:44:55:66",
			expected: true,
			desc:     "Should match third TUYA OUI prefix",
		},
		{
			name:     "non-TUYA device",
			clientIP: "192.168.1.200",
			mac:      "aa:bb:cc:dd:ee:ff",
			expected: false,
			desc:     "Should not match non-TUYA OUI",
		},
		{
			name:     "Google Nest device",
			clientIP: "192.168.1.201",
			mac:      "64:16:66:aa:bb:cc",
			expected: false,
			desc:     "Should not match Google Nest OUI",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up MAC cache for this test
			macCache.Set(tt.clientIP, tt.mac, macCacheTTL)

			result := ShouldUsePublicServers(tt.clientIP)
			if result != tt.expected {
				t.Errorf("%s: ShouldUsePublicServers(%s) with MAC %s = %v, want %v",
					tt.desc, tt.clientIP, tt.mac, result, tt.expected)
			}
		})
	}
}

func TestShouldUsePublicServers_Disabled(t *testing.T) {
	resetRoutingMaps()

	// Set up some routing rules
	PublicOnlyClientsMap.Store("192.168.1.100", true)
	PublicOnlyClientMACsMap.Store("00:11:22:33:44:55", true)
	PublicOnlyClientOUIMap.Store("68:57:2d", true)

	// Disable client routing
	cfg = &config.Config{EnableClientRouting: false}

	// None of these should trigger public server usage
	tests := []string{"192.168.1.100", "192.168.1.200", "10.0.0.1"}

	for _, ip := range tests {
		result := ShouldUsePublicServers(ip)
		if result {
			t.Errorf("ShouldUsePublicServers(%s) should return false when client routing is disabled", ip)
		}
	}
}

func TestShouldUsePublicServers_Priority(t *testing.T) {
	resetRoutingMaps()

	cfg = &config.Config{EnableClientRouting: true}

	testIP := "192.168.1.100"
	testMAC := "68:57:2d:aa:bb:cc"

	// Test 1: IP-based routing takes priority
	PublicOnlyClientsMap.Store(testIP, true)
	result1 := ShouldUsePublicServers(testIP)
	if !result1 {
		t.Error("IP-based routing should take priority and return true")
	}

	// Test 2: MAC-based routing when IP not in list
	resetRoutingMaps()
	PublicOnlyClientMACsMap.Store(testMAC, true)
	macCache.Set(testIP, testMAC, macCacheTTL)
	result2 := ShouldUsePublicServers(testIP)
	if !result2 {
		t.Error("MAC-based routing should work when IP not in list")
	}

	// Test 3: OUI-based routing when neither IP nor full MAC match
	resetRoutingMaps()
	PublicOnlyClientOUIMap.Store("68:57:2d", true)
	macCache.Set(testIP, testMAC, macCacheTTL)
	result3 := ShouldUsePublicServers(testIP)
	if !result3 {
		t.Error("OUI-based routing should work when IP and MAC not in list")
	}
}

func TestOUIMatching_EdgeCases(t *testing.T) {
	resetRoutingMaps()

	cfg = &config.Config{EnableClientRouting: true}

	// Test with invalid/edge case MAC addresses
	tests := []struct {
		name     string
		mac      string
		oui      string
		expected bool
	}{
		{
			name:     "empty MAC",
			mac:      "",
			oui:      "68:57:2d",
			expected: false,
		},
		{
			name:     "incomplete MAC (less than 3 octets)",
			mac:      "68:57",
			oui:      "68:57:2d",
			expected: false,
		},
		{
			name:     "MAC without separators",
			mac:      "68572daa bbcc",
			oui:      "68:57:2d",
			expected: false,
		},
		{
			name:     "valid MAC with mixed separators",
			mac:      "68-57-2d:aa:bb:cc",
			oui:      "68:57:2d",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetRoutingMaps()
			PublicOnlyClientOUIMap.Store(util.NormalizeMAC(tt.oui), true)

			testIP := "192.168.1.100"
			macCache.Set(testIP, tt.mac, macCacheTTL)

			result := ShouldUsePublicServers(testIP)
			if result != tt.expected {
				t.Errorf("OUI matching for MAC %s with OUI %s = %v, want %v",
					tt.mac, tt.oui, result, tt.expected)
			}
		})
	}
}

func BenchmarkShouldUsePublicServers_IPOnly(b *testing.B) {
	resetRoutingMaps()
	cfg = &config.Config{EnableClientRouting: true}

	PublicOnlyClientsMap.Store("192.168.1.100", true)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ShouldUsePublicServers("192.168.1.100")
	}
}

func BenchmarkShouldUsePublicServers_WithMAC(b *testing.B) {
	resetRoutingMaps()
	cfg = &config.Config{EnableClientRouting: true}

	testIP := "192.168.1.100"
	testMAC := "68:57:2d:aa:bb:cc"
	PublicOnlyClientMACsMap.Store(testMAC, true)
	macCache.Set(testIP, testMAC, macCacheTTL)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ShouldUsePublicServers(testIP)
	}
}

func BenchmarkShouldUsePublicServers_WithOUI(b *testing.B) {
	resetRoutingMaps()
	cfg = &config.Config{EnableClientRouting: true}

	testIP := "192.168.1.100"
	testMAC := "68:57:2d:aa:bb:cc"
	PublicOnlyClientOUIMap.Store("68:57:2d", true)
	macCache.Set(testIP, testMAC, macCacheTTL)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ShouldUsePublicServers(testIP)
	}
}
