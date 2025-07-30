package domainrouting

import (
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	RoutingTable = make(map[string]string)
	cfg          = config.Get()
)

func InitializeDomainRouting() {
	if !cfg.EnableDomainRouting {
		return
	}
	// Check if the domain routing folder is specified
	if cfg.DomainRoutingFolder == "" {
		logutil.Logger.Fatalf("Domain routing is enabled but no folder is specified")
	}
	logutil.Logger.Infof("Domain routing enabled with folder: %v", cfg.DomainRoutingFolder)
	loadRoutingTable(cfg.DomainRoutingFolder)
	if len(RoutingTable) == 0 {
		logutil.Logger.Fatalf("No domain routing entries found in folder: %s", cfg.DomainRoutingFolder)
	}
	// refresh routing table every DomainRoutingTableReloadInterval seconds
	logutil.Logger.Debugf("Domain routing table will be refreshed every %d seconds", cfg.DomainRoutingTableReloadInterval)
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.DomainRoutingTableReloadInterval) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			loadRoutingTable(cfg.DomainRoutingFolder)
			if len(RoutingTable) == 0 {
				logutil.Logger.Fatalf("No domain routing entries found after refresh in folder: %s", cfg.DomainRoutingFolder)
			}
		}
	}()
	logutil.Logger.Infof("Domain routing initialized successfully")
	logutil.Logger.Infof("Routing table size: %d", len(RoutingTable))
}

func loadRoutingTable(folder string) {
	var wg sync.WaitGroup
	var mu sync.Mutex

	files, err := os.ReadDir(folder)
	if err != nil {
		logutil.Logger.Fatalf("Error reading domain routing folder %s: %v", folder, err)
	}
	// keep only .txt files
	var txtFiles []string
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".txt") {
			txtFiles = append(txtFiles, file.Name())
		}
	}
	if len(txtFiles) == 0 {
		logutil.Logger.Fatalf("No .txt files found in domain routing folder %s", folder)
	}
	for _, file := range txtFiles {
		wg.Add(1)
		go func(file string) {
			defer wg.Done()
			fullPath := folder + "/" + file
			fileInfo, err := os.Stat(fullPath)
			if err != nil {
				logutil.Logger.Fatalf("Domain routing file %s error: %v", fullPath, err)
			}
			if fileInfo.IsDir() {
				logutil.Logger.Fatalf("Domain routing file %s is a directory", fullPath)
			}
			fileContent, err := os.ReadFile(fullPath)
			if err != nil {
				logutil.Logger.Fatalf("Error reading domain routing file %s: %v", fullPath, err)
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
