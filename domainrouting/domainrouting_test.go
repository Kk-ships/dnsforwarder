package domainrouting

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestProcessLine(t *testing.T) {
	tests := []struct {
		input        string
		expectDomain string
		expectIP     string
		expectValid  bool
	}{
		{"#comment", "", "", false},
		{"", "", "", false},
		{"  ", "", "", false},
		{"/example.com/8.8.8.8", "example.com.", "8.8.8.8:53", true},
		{"/example.com./8.8.8.8:53", "example.com.", "8.8.8.8:53", true},
		{"/invalid", "", "", false},
		{"/example.com/", "", "", false},
		{"//8.8.8.8", "", "", false},
		{"/example.com/invalid-ip", "", "", false},
		{"/example.com/8.8.8.8:53", "example.com.", "8.8.8.8:53", true},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("input_%s", tt.input), func(t *testing.T) {
			domain, ip, valid := processLine(tt.input)
			if domain != tt.expectDomain || ip != tt.expectIP || valid != tt.expectValid {
				t.Errorf("processLine(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.input, domain, ip, valid, tt.expectDomain, tt.expectIP, tt.expectValid)
			}
		})
	}
}

func TestValidateDomain(t *testing.T) {
	tests := []struct {
		domain string
		valid  bool
	}{
		{"example.com", true},
		{"sub.example.com", true},
		{"", false},
		{"no-dot", false},
		{string(make([]byte, 254)), false}, // too long
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			if result := validateDomain(tt.domain); result != tt.valid {
				t.Errorf("validateDomain(%q) = %v, want %v", tt.domain, result, tt.valid)
			}
		})
	}
}

func TestValidateIPPort(t *testing.T) {
	tests := []struct {
		address string
		valid   bool
	}{
		{"8.8.8.8:53", true},
		{"1.1.1.1:53", true},
		{"[::1]:53", true},
		{"8.8.8.8:80", false},
		{"8.8.8.8", false},
		{"invalid:53", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			if result := validateIPPort(tt.address); result != tt.valid {
				t.Errorf("validateIPPort(%q) = %v, want %v", tt.address, result, tt.valid)
			}
		})
	}
}

func TestLoadRoutingTable(t *testing.T) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "routing_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			t.Errorf("Failed to remove temp dir: %v", err)
		}
	}()

	// Create test files
	testFile1 := filepath.Join(tempDir, "test1.txt")
	testContent1 := `# Test file 1
/example.com/8.8.8.8
/test.com/1.1.1.1:53
# Another comment
/invalid-line
/valid.example/9.9.9.9
`
	if err := os.WriteFile(testFile1, []byte(testContent1), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	testFile2 := filepath.Join(tempDir, "test2.txt")
	testContent2 := `/another.com/8.8.4.4
/duplicate.com/1.1.1.1
`
	if err := os.WriteFile(testFile2, []byte(testContent2), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Create non-.txt file (should be ignored)
	nonTxtFile := filepath.Join(tempDir, "ignore.log")
	if err := os.WriteFile(nonTxtFile, []byte("/should.ignore/1.1.1.1"), 0644); err != nil {
		t.Fatalf("Failed to write non-txt file: %v", err)
	}

	// Test loading
	table, err := loadRoutingTable(tempDir)
	if err != nil {
		t.Fatalf("loadRoutingTable failed: %v", err)
	}

	expectedEntries := map[string]string{
		"example.com.":   "8.8.8.8:53",
		"test.com.":      "1.1.1.1:53",
		"valid.example.": "9.9.9.9:53",
		"another.com.":   "8.8.4.4:53",
		"duplicate.com.": "1.1.1.1:53",
	}

	if len(table) != len(expectedEntries) {
		t.Errorf("Expected %d entries, got %d", len(expectedEntries), len(table))
	}

	for domain, expectedIP := range expectedEntries {
		if actualIP, exists := table[domain]; !exists {
			t.Errorf("Missing domain %s", domain)
		} else if actualIP != expectedIP {
			t.Errorf("Domain %s: expected %s, got %s", domain, expectedIP, actualIP)
		}
	}
}

func TestGetRoutingTableAndLookup(t *testing.T) {
	// Initialize with empty table
	emptyTable := make(map[string]string)
	routingTablePtr.Store(&emptyTable)

	// Test empty table
	table := GetRoutingTable()
	if len(table) != 0 {
		t.Errorf("Expected empty table, got %d entries", len(table))
	}

	_, exists := LookupDomain("example.com.")
	if exists {
		t.Errorf("Expected domain not to exist in empty table")
	}

	// Add some entries
	testTable := map[string]string{
		"example.com.": "8.8.8.8:53",
		"test.com.":    "1.1.1.1:53",
	}
	routingTablePtr.Store(&testTable)

	// Test lookup
	server, exists := LookupDomain("example.com.")
	if !exists {
		t.Errorf("Expected domain to exist")
	}
	if server != "8.8.8.8:53" {
		t.Errorf("Expected 8.8.8.8:53, got %s", server)
	}

	// Test non-existent domain
	_, exists = LookupDomain("nonexistent.com.")
	if exists {
		t.Errorf("Expected domain not to exist")
	}

	// Test GetRoutingTable returns copy
	table = GetRoutingTable()
	if len(table) != 2 {
		t.Errorf("Expected 2 entries, got %d", len(table))
	}

	// Modifying returned table should not affect original
	table["modified.com."] = "127.0.0.1:53"

	newTable := GetRoutingTable()
	if len(newTable) != 2 {
		t.Errorf("Original table should not be modified, expected 2 entries, got %d", len(newTable))
	}
}

// Benchmark tests to demonstrate performance improvements
func BenchmarkProcessLine(b *testing.B) {
	line := "/example.com/8.8.8.8"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processLine(line)
	}
}

func BenchmarkLookupDomain(b *testing.B) {
	// Setup test table
	testTable := make(map[string]string)
	for i := 0; i < 1000; i++ {
		testTable[fmt.Sprintf("domain%d.com.", i)] = "8.8.8.8:53"
	}
	routingTablePtr.Store(&testTable)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		LookupDomain("domain500.com.")
	}
}
