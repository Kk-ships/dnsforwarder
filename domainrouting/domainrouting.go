package domainrouting

import (
	"bufio"
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// Use atomic.Value for thread-safe map replacement
	routingTablePtr atomic.Value // holds *map[string]string
	cfg             = config.Get()
)

// GetRoutingTable returns a copy of the current routing table
func GetRoutingTable() map[string]string {
	if table := routingTablePtr.Load(); table != nil {
		originalTable := *table.(*map[string]string)
		// Create a proper copy of the map
		copyTable := make(map[string]string, len(originalTable))
		for k, v := range originalTable {
			copyTable[k] = v
		}
		return copyTable
	}
	return make(map[string]string)
}

// LookupDomain returns the DNS server for a given domain
func LookupDomain(domain string) (string, bool) {
	if table := routingTablePtr.Load(); table != nil {
		tableMap := *table.(*map[string]string)
		server, exists := tableMap[domain]
		return server, exists
	}
	return "", false
}

func InitializeDomainRouting() {
	if !cfg.EnableDomainRouting {
		return
	}
	// Check if the domain routing folder is specified
	if cfg.DomainRoutingFolder == "" {
		logutil.Logger.Errorf("Domain routing is enabled but no folder is specified - disabling domain routing")
		return
	}
	logutil.Logger.Infof("Domain routing enabled with folder: %v", cfg.DomainRoutingFolder)

	// Initial load
	table, err := loadRoutingTable(cfg.DomainRoutingFolder)
	if err != nil {
		logutil.Logger.Errorf("Failed to load initial routing table: %v - disabling domain routing", err)
		return
	}
	if len(table) == 0 {
		logutil.Logger.Errorf("No domain routing entries found in folder: %s - disabling domain routing", cfg.DomainRoutingFolder)
		return
	}
	routingTablePtr.Store(&table)

	// refresh routing table every DomainRoutingTableReloadInterval seconds
	logutil.Logger.Debugf("Domain routing table will be refreshed every %d seconds", cfg.DomainRoutingTableReloadInterval)
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.DomainRoutingTableReloadInterval) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			table, err := loadRoutingTable(cfg.DomainRoutingFolder)
			if err != nil {
				logutil.Logger.Errorf("Failed to reload routing table: %v", err)
				continue
			}
			if len(table) == 0 {
				logutil.Logger.Errorf("No domain routing entries found after refresh in folder: %s", cfg.DomainRoutingFolder)
				continue
			}
			routingTablePtr.Store(&table)
			logutil.Logger.Debugf("Routing table reloaded successfully with %d entries", len(table))
		}
	}()
	logutil.Logger.Infof("Domain routing initialized successfully")
	logutil.Logger.Infof("Routing table size: %d", len(table))
}

// validateDomain checks if a domain is valid
func validateDomain(domain string) bool {
	if domain == "" || len(domain) > 253 {
		return false
	}
	// Basic domain validation - could be more comprehensive
	return strings.Contains(domain, ".")
}

// validateIPPort checks if an IP:port combination is valid
func validateIPPort(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if port != "53" {
		return false
	}
	return net.ParseIP(host) != nil
}

// processLine parses a single line from routing file
func processLine(line string) (domain, ip string, valid bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}

	parts := strings.Split(line, "/")
	if len(parts) < 3 {
		return "", "", false
	}

	domain, ip = strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	if domain == "" || ip == "" {
		return "", "", false
	}

	// Normalize domain - add trailing dot if not present
	if !strings.HasSuffix(domain, ".") {
		domain += "."
	}

	// Normalize IP - add :53 if port not specified
	if !strings.Contains(ip, ":") {
		ip += ":53"
	} else if strings.Contains(ip, "::") && !strings.HasPrefix(ip, "[") {
		// Handle IPv6 addresses - wrap in brackets if not already done
		ip = "[" + ip + "]:53"
	}

	// Validate domain and IP
	if !validateDomain(domain) || !validateIPPort(ip) {
		return "", "", false
	}

	return domain, ip, true
}

// loadSingleFile processes a single routing file
func loadSingleFile(filePath string, resultChan chan<- map[string]string, errChan chan<- error) {
	localTable := make(map[string]string)

	file, err := os.Open(filePath)
	if err != nil {
		errChan <- fmt.Errorf("error opening file %s: %w", filePath, err)
		return
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			logutil.Logger.Errorf("Error closing file %s: %v", filePath, closeErr)
		}
	}()

	scanner := bufio.NewScanner(file)
	lineCount := 0
	for scanner.Scan() {
		lineCount++
		domain, ip, valid := processLine(scanner.Text())
		if valid {
			localTable[domain] = ip
		}
	}

	if err := scanner.Err(); err != nil {
		errChan <- fmt.Errorf("error reading file %s: %w", filePath, err)
		return
	}

	logutil.Logger.Debugf("Processed file %s: %d lines, %d valid entries", filePath, lineCount, len(localTable))
	resultChan <- localTable
}

// loadRoutingTable loads domain routing table from folder
func loadRoutingTable(folder string) (map[string]string, error) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, fmt.Errorf("error reading domain routing folder %s: %w", folder, err)
	}

	// Filter .txt files more efficiently
	txtFiles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".txt") {
			txtFiles = append(txtFiles, entry.Name())
		}
	}

	if len(txtFiles) == 0 {
		return nil, fmt.Errorf("no .txt files found in domain routing folder %s", folder)
	}

	// Use channels for collecting results and errors
	resultChan := make(chan map[string]string, len(txtFiles))
	errChan := make(chan error, len(txtFiles))

	var wg sync.WaitGroup

	// Process files concurrently
	for _, file := range txtFiles {
		wg.Add(1)
		go func(fileName string) {
			defer wg.Done()
			fullPath := filepath.Join(folder, fileName)
			loadSingleFile(fullPath, resultChan, errChan)
		}(file)
	}

	// Wait for all goroutines to complete
	go func() {
		wg.Wait()
		close(resultChan)
		close(errChan)
	}()

	// Collect results and errors
	finalTable := make(map[string]string)
	var errors []error

	// Collect all results
	for localTable := range resultChan {
		for domain, ip := range localTable {
			finalTable[domain] = ip
		}
	}

	// Collect all errors
	for err := range errChan {
		errors = append(errors, err)
	}

	// Return first error if any occurred
	if len(errors) > 0 {
		return nil, errors[0]
	}

	logutil.Logger.Debugf("Loaded routing table with %d total entries from %d files", len(finalTable), len(txtFiles))
	return finalTable, nil
}
