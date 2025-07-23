package domainrouting

import (
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"os"
	"strings"
	"sync"
	"time"
)

var RoutingTable = make(map[string]string)

func InitializeDomainRouting() {
	if !config.EnableDomainRouting {
		return
	}
	// Check if the domain routing folder is specified
	if config.DomainRoutingFolder == "" {
		logutil.LogWithBufferf("Domain routing is enabled but no folder is specified")
		os.Exit(1)
	}
	logutil.LogWithBufferf("Domain routing enabled with folder: %v", config.DomainRoutingFolder)
	loadRoutingTable(config.DomainRoutingFolder)
	if len(RoutingTable) == 0 {
		logutil.LogWithBufferf("No domain routing entries found in folder: %s", config.DomainRoutingFolder)
		os.Exit(1)
	}
	// refresh routing table every DomainRoutingTableReloadInterval seconds
	logutil.LogWithBufferf("Domain routing table will be refreshed every %d seconds", config.DomainRoutingTableReloadInterval)
	go func() {
		ticker := time.NewTicker(config.DomainRoutingTableReloadInterval * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			logutil.LogWithBufferf("Refreshing domain routing table from folder: %s", config.DomainRoutingFolder)
			loadRoutingTable(config.DomainRoutingFolder)
			if len(RoutingTable) == 0 {
				logutil.LogWithBufferf("No domain routing entries found after refresh in folder: %s", config.DomainRoutingFolder)
				os.Exit(1)
			}
			logutil.LogWithBufferf("Domain routing table refreshed successfully, size: %d", len(RoutingTable))
		}
	}()
	logutil.LogWithBufferf("Domain routing initialized successfully")
	logutil.LogWithBufferf("Routing table size: %d", len(RoutingTable))
}

func loadRoutingTable(folder string) {
	var wg sync.WaitGroup
	var mu sync.Mutex

	files, err := os.ReadDir(folder)
	if err != nil {
		logutil.LogWithBufferf("Error reading domain routing folder %s: %v", folder, err)
		os.Exit(1)
	}
	// keep only .txt files
	var txtFiles []string
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".txt") {
			txtFiles = append(txtFiles, file.Name())
		}
	}
	if len(txtFiles) == 0 {
		logutil.LogWithBufferf("No .txt files found in domain routing folder %s", folder)
		os.Exit(1)
	}
	for _, file := range txtFiles {
		wg.Add(1)
		go func(file string) {
			defer wg.Done()
			fileInfo, err := os.Stat(file)
			if err != nil {
				logutil.LogWithBufferf("Domain routing file %s error: %v", file, err)
				os.Exit(1)
			}
			if fileInfo.IsDir() {
				logutil.LogWithBufferf("Domain routing file %s is a directory", file)
				os.Exit(1)
			}
			logutil.LogWithBufferf("Loading domain routing configuration from %s", file)
			fileContent, err := os.ReadFile(file)
			if err != nil {
				logutil.LogWithBufferf("Error reading domain routing file %s: %v", file, err)
				os.Exit(1)
			}
			lines := strings.SplitSeq(string(fileContent), "\n")
			for line := range lines {
				line = strings.TrimSpace(line)
				if line != "" && line[0] == '#' {
					continue
				}
				parts := strings.Split(line, "/")
				if len(parts) < 3 {
					continue
				}
				domain, ip := parts[1], parts[2]
				if domain == "" || ip == "" {
					continue
				}
				// add '.' to domain at end if not present
				if !strings.HasSuffix(domain, ".") {
					domain += "."
				}
				// add ':53' to IP end if not present to ensure it's a valid DNS server address
				if !strings.HasSuffix(ip, ":53") {
					logutil.LogWithBufferf("[DEBUG] Adding port 53 to IP %s for domain %s", ip, domain)
					ip += ":53"
				}
				mu.Lock()
				RoutingTable[domain] = ip
				mu.Unlock()
			}
		}(file)
	}
	wg.Wait()
}
