package domainrouting

import (
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"os"
	"strings"
	"sync"
)

var RoutingTable = make(map[string]string)

func InitializeDomainRouting() {
	if !config.EnableDomainRouting {
		return
	}
	// domain routing logic
	if len(config.DomainRoutingFiles) == 0 {
		logutil.LogWithBufferf("Domain routing is enabled but no configuration files are specified")
		os.Exit(1)
	}
	logutil.LogWithBufferf("Domain routing enabled with files: %v", config.DomainRoutingFiles)
	loadRoutingTable(config.DomainRoutingFiles)
	logutil.LogWithBufferf("Domain routing initialized successfully")
	logutil.LogWithBufferf("Routing table size: %d", len(RoutingTable))
}

// splitCharacter splits a string into substrings based on a delimiter.
func splitCharacter(s string, character string) []string {
	var substrings []string
	start := 0
	for i := 0; i < len(s); i++ {
		if string(s[i]) == character {
			substrings = append(substrings, s[start:i])
			start = i + 1
		}
	}
	substrings = append(substrings, s[start:])
	return substrings
}

func loadRoutingTable(files []string) {
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, file := range files {
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
			lines := splitCharacter(string(fileContent), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || line[0] == '#' {
					continue
				}
				parts := splitCharacter(line, "/")
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
