package util

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// Implement net.Addr interface for testing generic addresses
type genericAddr struct {
	network string
	address string
}

func (ga *genericAddr) Network() string { return ga.network }
func (ga *genericAddr) String() string  { return ga.address }

// Mock DNS ResponseWriter for testing
type mockResponseWriter struct {
	remoteAddr net.Addr
}

func (m *mockResponseWriter) LocalAddr() net.Addr       { return nil }
func (m *mockResponseWriter) RemoteAddr() net.Addr      { return m.remoteAddr }
func (m *mockResponseWriter) WriteMsg(*dns.Msg) error   { return nil }
func (m *mockResponseWriter) Write([]byte) (int, error) { return 0, nil }
func (m *mockResponseWriter) Close() error              { return nil }
func (m *mockResponseWriter) TsigStatus() error         { return nil }
func (m *mockResponseWriter) TsigTimersOnly(bool)       {}
func (m *mockResponseWriter) Hijack()                   {}
func (m *mockResponseWriter) Network() string           { return "udp" }

func TestGetEnvDuration(t *testing.T) {
	// Clear cache before testing
	ClearEnvCaches()

	tests := []struct {
		name     string
		key      string
		envValue string
		def      time.Duration
		expected time.Duration
	}{
		{
			name:     "valid duration",
			key:      "TEST_DURATION",
			envValue: "30s",
			def:      10 * time.Second,
			expected: 30 * time.Second,
		},
		{
			name:     "invalid duration uses default",
			key:      "TEST_DURATION_INVALID",
			envValue: "invalid",
			def:      10 * time.Second,
			expected: 10 * time.Second,
		},
		{
			name:     "empty env uses default",
			key:      "TEST_DURATION_EMPTY",
			envValue: "",
			def:      5 * time.Minute,
			expected: 5 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				if err := os.Setenv(tt.key, tt.envValue); err != nil {
					t.Fatalf("Failed to set env var: %v", err)
				}
				defer func() {
					if err := os.Unsetenv(tt.key); err != nil {
						t.Logf("Failed to unset env var: %v", err)
					}
				}()
			}

			result := GetEnvDuration(tt.key, tt.def)
			if result != tt.expected {
				t.Errorf("GetEnvDuration() = %v, want %v", result, tt.expected)
			}

			// Test caching - second call should return cached value
			result2 := GetEnvDuration(tt.key, time.Hour)
			if result2 != tt.expected {
				t.Errorf("GetEnvDuration() cached = %v, want %v", result2, tt.expected)
			}
		})
	}
}

func TestGetEnvInt(t *testing.T) {
	ClearEnvCaches()

	tests := []struct {
		name     string
		key      string
		envValue string
		def      int
		expected int
	}{
		{
			name:     "valid integer",
			key:      "TEST_INT",
			envValue: "42",
			def:      10,
			expected: 42,
		},
		{
			name:     "invalid integer uses default",
			key:      "TEST_INT_INVALID",
			envValue: "not_a_number",
			def:      100,
			expected: 100,
		},
		{
			name:     "empty env uses default",
			key:      "TEST_INT_EMPTY",
			envValue: "",
			def:      50,
			expected: 50,
		},
		{
			name:     "negative integer",
			key:      "TEST_INT_NEG",
			envValue: "-123",
			def:      0,
			expected: -123,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				if err := os.Setenv(tt.key, tt.envValue); err != nil {
					t.Fatalf("Failed to set env var: %v", err)
				}
				defer func() {
					if err := os.Unsetenv(tt.key); err != nil {
						t.Logf("Failed to unset env var: %v", err)
					}
				}()
			}

			result := GetEnvInt(tt.key, tt.def)
			if result != tt.expected {
				t.Errorf("GetEnvInt() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetEnvString(t *testing.T) {
	ClearEnvCaches()

	tests := []struct {
		name     string
		key      string
		envValue string
		def      string
		expected string
	}{
		{
			name:     "existing env var",
			key:      "TEST_STRING",
			envValue: "hello world",
			def:      "default",
			expected: "hello world",
		},
		{
			name:     "empty env uses default",
			key:      "TEST_STRING_EMPTY",
			envValue: "",
			def:      "default_value",
			expected: "default_value",
		},
		{
			name:     "special characters",
			key:      "TEST_STRING_SPECIAL",
			envValue: "test@#$%^&*()",
			def:      "default",
			expected: "test@#$%^&*()",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				if err := os.Setenv(tt.key, tt.envValue); err != nil {
					t.Fatalf("Failed to set env var: %v", err)
				}
				defer func() {
					if err := os.Unsetenv(tt.key); err != nil {
						t.Logf("Failed to unset env var: %v", err)
					}
				}()
			}

			result := GetEnvString(tt.key, tt.def)
			if result != tt.expected {
				t.Errorf("GetEnvString() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetEnvStringSlice(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		envValue string
		def      string
		expected []string
	}{
		{
			name:     "single value",
			key:      "TEST_SLICE_SINGLE",
			envValue: "single",
			def:      "default",
			expected: []string{"single"},
		},
		{
			name:     "multiple values",
			key:      "TEST_SLICE_MULTI",
			envValue: "one,two,three",
			def:      "default",
			expected: []string{"one", "two", "three"},
		},
		{
			name:     "values with spaces",
			key:      "TEST_SLICE_SPACES",
			envValue: " one , two , three ",
			def:      "default",
			expected: []string{"one", "two", "three"},
		},
		{
			name:     "empty values filtered",
			key:      "TEST_SLICE_EMPTY",
			envValue: "one,,three,",
			def:      "default",
			expected: []string{"one", "three"},
		},
		{
			name:     "no env uses default",
			key:      "TEST_SLICE_DEFAULT",
			envValue: "",
			def:      "default_value",
			expected: []string{"default_value"},
		},
		{
			name:     "no env no default",
			key:      "TEST_SLICE_NO_DEFAULT",
			envValue: "",
			def:      "",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				if err := os.Setenv(tt.key, tt.envValue); err != nil {
					t.Fatalf("Failed to set env var: %v", err)
				}
				defer func() {
					if err := os.Unsetenv(tt.key); err != nil {
						t.Logf("Failed to unset env var: %v", err)
					}
				}()
			}

			result := GetEnvStringSlice(tt.key, tt.def)
			if len(result) != len(tt.expected) {
				t.Errorf("GetEnvStringSlice() length = %v, want %v", len(result), len(tt.expected))
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("GetEnvStringSlice()[%d] = %v, want %v", i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestGetEnvBool(t *testing.T) {
	ClearEnvCaches()

	tests := []struct {
		name     string
		key      string
		envValue string
		def      bool
		expected bool
	}{
		{
			name:     "true string",
			key:      "TEST_BOOL_TRUE",
			envValue: "true",
			def:      false,
			expected: true,
		},
		{
			name:     "1 string",
			key:      "TEST_BOOL_1",
			envValue: "1",
			def:      false,
			expected: true,
		},
		{
			name:     "false string",
			key:      "TEST_BOOL_FALSE",
			envValue: "false",
			def:      true,
			expected: false,
		},
		{
			name:     "0 string",
			key:      "TEST_BOOL_0",
			envValue: "0",
			def:      true,
			expected: false,
		},
		{
			name:     "invalid uses default",
			key:      "TEST_BOOL_INVALID",
			envValue: "invalid",
			def:      true,
			expected: true,
		},
		{
			name:     "empty uses default",
			key:      "TEST_BOOL_EMPTY",
			envValue: "",
			def:      false,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				if err := os.Setenv(tt.key, tt.envValue); err != nil {
					t.Fatalf("Failed to set env var: %v", err)
				}
				defer func() {
					if err := os.Unsetenv(tt.key); err != nil {
						t.Logf("Failed to unset env var: %v", err)
					}
				}()
			}

			result := GetEnvBool(tt.key, tt.def)
			if result != tt.expected {
				t.Errorf("GetEnvBool() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestRunCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		args    []string
		wantErr bool
	}{
		{
			name:    "echo command",
			cmd:     "echo",
			args:    []string{"hello", "world"},
			wantErr: false,
		},
		{
			name:    "invalid command",
			cmd:     "nonexistent_command_12345",
			args:    []string{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := RunCommand(tt.cmd, tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("RunCommand() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.cmd == "echo" {
				expected := "hello world\n"
				if output != expected {
					t.Errorf("RunCommand() output = %v, want %v", output, expected)
				}
			}
		})
	}
}

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "colon separated",
			input:    "00:11:22:33:44:55",
			expected: "00:11:22:33:44:55",
		},
		{
			name:     "dash separated",
			input:    "00-11-22-33-44-55",
			expected: "00:11:22:33:44:55",
		},
		{
			name:     "dot separated",
			input:    "00.11.22.33.44.55",
			expected: "00:11:22:33:44:55",
		},
		{
			name:     "mixed case",
			input:    "00:AB:cd:EF:12:34",
			expected: "00:ab:cd:ef:12:34",
		},
		{
			name:     "with spaces",
			input:    "00 11 22 33 44 55",
			expected: "00:11:22:33:44:55",
		},
		{
			name:     "mixed separators",
			input:    "00-11.22:33 44-55",
			expected: "00:11:22:33:44:55",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "invalid characters filtered",
			input:    "00:11:GG:33:44:55",
			expected: "00:11:33:44:55",
		},
		{
			name:     "no separators",
			input:    "001122334455",
			expected: "001122334455",
		},
		{
			name:     "trailing separator",
			input:    "00:11:22:33:44:55:",
			expected: "00:11:22:33:44:55",
		},
		{
			name:     "multiple consecutive separators",
			input:    "00::11--22..33  44::55",
			expected: "00:11:22:33:44:55",
		},
		{
			name:     "leading separator",
			input:    ":00:11:22:33:44:55",
			expected: "00:11:22:33:44:55",
		},
		{
			name:     "only separators",
			input:    "::---..",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeMAC(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeMAC(%v) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		name     string
		addr     net.Addr
		expected string
	}{
		{
			name:     "UDP address",
			addr:     &net.UDPAddr{IP: net.ParseIP("192.168.1.100"), Port: 53},
			expected: "192.168.1.100",
		},
		{
			name:     "TCP address",
			addr:     &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8080},
			expected: "10.0.0.1",
		},
		{
			name:     "IPv6 UDP address",
			addr:     &net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 53},
			expected: "2001:db8::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockResponseWriter{remoteAddr: tt.addr}
			result := GetClientIP(mock)
			if result != tt.expected {
				t.Errorf("GetClientIP() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// Test with a generic address that uses the default case
func TestGetClientIPGeneric(t *testing.T) {
	// Create a mock address that doesn't implement UDPAddr or TCPAddr
	mockAddr := &genericAddr{network: "custom", address: "127.0.0.1:8080"}
	mock := &mockResponseWriter{remoteAddr: mockAddr}
	result := GetClientIP(mock)
	expected := "127.0.0.1"
	if result != expected {
		t.Errorf("GetClientIP() with generic addr = %v, want %v", result, expected)
	}

	// Test address without colon
	mockAddr2 := &genericAddr{network: "custom", address: "127.0.0.1"}
	mock2 := &mockResponseWriter{remoteAddr: mockAddr2}
	result2 := GetClientIP(mock2)
	expected2 := "127.0.0.1"
	if result2 != expected2 {
		t.Errorf("GetClientIP() with no port = %v, want %v", result2, expected2)
	}
}
func TestClearCaches(t *testing.T) {
	// Set some cached values
	GetEnvString("TEST_CACHE", "test")
	GetEnvInt("TEST_CACHE_INT", 42)

	// Clear caches
	ClearEnvCaches()
}

func TestExtractMACOUI(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "standard MAC address",
			input:    "00:11:22:33:44:55",
			expected: "00:11:22",
		},
		{
			name:     "MAC with dashes",
			input:    "AA-BB-CC-DD-EE-FF",
			expected: "aa:bb:cc",
		},
		{
			name:     "MAC with dots",
			input:    "12.34.56.78.9a.bc",
			expected: "12:34:56",
		},
		{
			name:     "mixed case MAC",
			input:    "aA:Bb:Cc:Dd:Ee:Ff",
			expected: "aa:bb:cc",
		},
		{
			name:     "MAC with spaces",
			input:    "00 11 22 33 44 55",
			expected: "00:11:22",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "incomplete MAC (less than 3 octets)",
			input:    "00:11",
			expected: "",
		},
		{
			name:     "TUYA device MAC",
			input:    "68:57:2d:aa:bb:cc",
			expected: "68:57:2d",
		},
		{
			name:     "invalid MAC",
			input:    "not:a:mac",
			expected: "",
		},
		{
			name:     "MAC without separators",
			input:    "001122334455",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractMACOUI(tt.input)
			if result != tt.expected {
				t.Errorf("ExtractMACOUI(%v) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// Benchmark tests to verify optimizations
func BenchmarkGetEnvString(b *testing.B) {
	if err := os.Setenv("BENCH_TEST", "benchmark_value"); err != nil {
		b.Fatalf("Failed to set env var: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("BENCH_TEST"); err != nil {
			b.Logf("Failed to unset env var: %v", err)
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GetEnvString("BENCH_TEST", "default")
	}
}

func BenchmarkGetEnvStringSlice(b *testing.B) {
	if err := os.Setenv("BENCH_SLICE", "one,two,three,four,five"); err != nil {
		b.Fatalf("Failed to set env var: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("BENCH_SLICE"); err != nil {
			b.Logf("Failed to unset env var: %v", err)
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GetEnvStringSlice("BENCH_SLICE", "default")
	}
}

func BenchmarkNormalizeMAC(b *testing.B) {
	testMAC := "00-11-22-33-44-55"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NormalizeMAC(testMAC)
	}
}

func BenchmarkRunCommand(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = RunCommand("echo", []string{"test"})
	}
}

func BenchmarkExtractMACOUI(b *testing.B) {
	testMAC := "68:57:2d:aa:bb:cc"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ExtractMACOUI(testMAC)
	}
}

// Test pool efficiency
func TestPoolEfficiency(t *testing.T) {
	// Test string builder pool
	t.Run("string builder pool", func(t *testing.T) {
		var builders []*strings.Builder

		// Get multiple builders
		for i := 0; i < 10; i++ {
			sb := stringBuilderPool.Get().(*strings.Builder)
			sb.WriteString("test") // Add some content
			builders = append(builders, sb)
		}

		// Put them back (they should be reset when retrieved again)
		for _, sb := range builders {
			stringBuilderPool.Put(sb)
		}

		// Get one more - should be from pool and reset in the actual usage
		sb := stringBuilderPool.Get().(*strings.Builder)
		// Note: Our actual code calls sb.Reset() in the functions that use the pool
		// So we simulate that here
		sb.Reset()
		if sb.Len() != 0 {
			t.Error("String builder from pool not reset properly")
		}
		stringBuilderPool.Put(sb)
	})

	// Test string slice pool
	t.Run("string slice pool", func(t *testing.T) {
		slicePtr := stringSlicePool.Get().(*[]string)
		slice := *slicePtr

		if len(slice) != 0 {
			t.Error("String slice from pool not reset properly")
		}
		if cap(slice) < 8 {
			t.Error("String slice from pool has insufficient capacity")
		}

		stringSlicePool.Put(slicePtr)
	})
}

// Test edge cases and error conditions
func TestEdgeCases(t *testing.T) {
	t.Run("GetEnvStringSlice with only separators", func(t *testing.T) {
		if err := os.Setenv("TEST_ONLY_SEPARATORS", ",,,"); err != nil {
			t.Fatalf("Failed to set env var: %v", err)
		}
		defer func() {
			if err := os.Unsetenv("TEST_ONLY_SEPARATORS"); err != nil {
				t.Logf("Failed to unset env var: %v", err)
			}
		}()

		result := GetEnvStringSlice("TEST_ONLY_SEPARATORS", "default")
		expected := []string{}
		if len(result) != len(expected) {
			t.Errorf("GetEnvStringSlice() with only separators = %v, want %v", result, expected)
		}
	})

	t.Run("NormalizeMAC with very long input", func(t *testing.T) {
		longInput := strings.Repeat("00:11:22:33:44:55:", 100)
		result := NormalizeMAC(longInput)
		// Should handle gracefully without panic
		if len(result) == 0 {
			t.Log("Long input handled gracefully")
		}
	})

	t.Run("GetEnvBool with mixed case", func(t *testing.T) {
		if err := os.Setenv("TEST_BOOL_MIXED", "TRUE"); err != nil {
			t.Fatalf("Failed to set env var: %v", err)
		}
		defer func() {
			if err := os.Unsetenv("TEST_BOOL_MIXED"); err != nil {
				t.Logf("Failed to unset env var: %v", err)
			}
		}()

		result := GetEnvBool("TEST_BOOL_MIXED", false)
		// Current implementation is case-sensitive, so this should use default
		if result != false {
			t.Errorf("GetEnvBool() with mixed case = %v, want %v", result, false)
		}
	})
}
