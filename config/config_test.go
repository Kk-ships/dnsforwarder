package config

import (
	"dnsloadbalancer/util"
	"os"
	"strings"
	"testing"
	"time"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		envVars     map[string]string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "default config should be valid",
			envVars:     map[string]string{},
			expectError: false,
		},
		{
			name: "invalid worker count should fail",
			envVars: map[string]string{
				"WORKER_COUNT": "0",
			},
			expectError: true,
			errorMsg:    "WORKER_COUNT must be positive",
		},
		{
			name: "invalid UDP size should fail",
			envVars: map[string]string{
				"UDP_SIZE": "100000",
			},
			expectError: true,
			errorMsg:    "UDP_SIZE must be between 1 and 65535",
		},
		{
			name: "invalid cache size should fail",
			envVars: map[string]string{
				"CACHE_SIZE": "-1",
			},
			expectError: true,
			errorMsg:    "CACHE_SIZE must be positive",
		},
		{
			name: "invalid DNS timeout should fail",
			envVars: map[string]string{
				"DNS_TIMEOUT": "-1s",
			},
			expectError: true,
			errorMsg:    "DNS_TIMEOUT must be positive",
		},
		{
			name: "invalid cache TTL should fail",
			envVars: map[string]string{
				"DNS_CACHE_TTL": "1s", // Below MinCacheTTL
			},
			expectError: true,
			errorMsg:    "DNS_CACHE_TTL must be between",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear caches and reset config before each test
			util.ClearEnvCaches()
			resetForTesting()

			// Set environment variables
			for key, value := range tt.envVars {
				if err := os.Setenv(key, value); err != nil {
					t.Fatalf("Failed to set environment variable %s: %v", key, err)
				}
				defer func(k string) {
					if err := os.Unsetenv(k); err != nil {
						t.Logf("Failed to unset environment variable %s: %v", k, err)
					}
				}(key)
			}

			// Create config and validate
			c := loadConfig()
			err := c.Validate()

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if tt.errorMsg != "" && !contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error message to contain '%s', got: %s", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}
		})
	}
}

func TestConfigSingleton(t *testing.T) {
	// Reset the config singleton
	util.ClearEnvCaches()
	resetForTesting()

	// Get config multiple times
	config1 := Get()
	config2 := Get()

	// Should be the same instance
	if config1 != config2 {
		t.Error("Config should be a singleton but got different instances")
	}
}

func TestConfigDefaults(t *testing.T) {
	// Reset the config singleton
	util.ClearEnvCaches()
	resetForTesting()

	config := Get()

	// Test some default values
	if config.DNSPort != ":53" {
		t.Errorf("Expected default DNS port to be ':53', got: %s", config.DNSPort)
	}

	if config.WorkerCount != 5 {
		t.Errorf("Expected default worker count to be 5, got: %d", config.WorkerCount)
	}

	if config.CacheTTL != 30*time.Minute {
		t.Errorf("Expected default cache TTL to be 30 minutes, got: %v", config.CacheTTL)
	}

	if !config.EnableMetrics {
		t.Error("Expected metrics to be enabled by default")
	}
}

func TestConfigEnvironmentOverrides(t *testing.T) {
	// Clear caches and reset config
	util.ClearEnvCaches()
	resetForTesting()

	// Set environment variables
	if err := os.Setenv("DNS_PORT", ":5353"); err != nil {
		t.Fatalf("Failed to set DNS_PORT: %v", err)
	}
	if err := os.Setenv("WORKER_COUNT", "10"); err != nil {
		t.Fatalf("Failed to set WORKER_COUNT: %v", err)
	}
	if err := os.Setenv("ENABLE_METRICS", "false"); err != nil {
		t.Fatalf("Failed to set ENABLE_METRICS: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("DNS_PORT"); err != nil {
			t.Logf("Failed to unset DNS_PORT: %v", err)
		}
		if err := os.Unsetenv("WORKER_COUNT"); err != nil {
			t.Logf("Failed to unset WORKER_COUNT: %v", err)
		}
		if err := os.Unsetenv("ENABLE_METRICS"); err != nil {
			t.Logf("Failed to unset ENABLE_METRICS: %v", err)
		}
	}()

	config := Get()

	// Test environment overrides
	if config.DNSPort != ":5353" {
		t.Errorf("Expected DNS port to be ':5353', got: %s", config.DNSPort)
	}

	if config.WorkerCount != 10 {
		t.Errorf("Expected worker count to be 10, got: %d", config.WorkerCount)
	}

	if config.EnableMetrics {
		t.Error("Expected metrics to be disabled")
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
