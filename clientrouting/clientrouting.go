package clientrouting

import (
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/util"
	"strings"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
)

var (
	PublicOnlyClientsMap    sync.Map // map[string]bool for fast lookup (IP)
	PublicOnlyClientMACsMap sync.Map // map[string]bool for fast lookup (MAC)

	// MAC address cache using go-cache
	macCache    *cache.Cache
	macCacheTTL = 5 * time.Minute // Cache MAC addresses for 5 minutes
	cfg         = config.Get()
)

func InitializeClientRouting() {
	if !cfg.EnableClientRouting {
		return
	}
	logutil.Logger.Info("Client-based DNS routing enabled")

	// Initialize MAC address cache
	macCache = cache.New(macCacheTTL, 2*macCacheTTL)

	storeClientsToMap(cfg.PublicOnlyClients, &PublicOnlyClientsMap, "IP")
	storeMACsToMap(cfg.PublicOnlyClientMACs, &PublicOnlyClientMACsMap)

	logutil.Logger.Debug("InitializeClientRouting: end")
}

func storeClientsToMap(clients []string, m *sync.Map, clientType string) {
	for _, client := range clients {
		if client != "" {
			client = strings.TrimSpace(client)
			m.Store(client, true)
			logutil.Logger.Debugf("Configured client %s to use public servers only (%s)", client, clientType)
		}
	}
	logutil.Logger.Debug("storeClientsToMap: end")
}

func storeMACsToMap(macs []string, m *sync.Map) {
	for _, mac := range macs {
		macNorm := util.NormalizeMAC(mac)
		if macNorm != "" {
			m.Store(macNorm, true)
			logutil.Logger.Debugf("Configured client %s to use public servers only (MAC)", macNorm)
		}
	}
	logutil.Logger.Debug("storeMACsToMap: end")
}

// getMACWithCache retrieves MAC address for IP with caching
func getMACWithCache(clientIP string) string {
	// Check if we have a cached entry
	if cachedMAC, found := macCache.Get(clientIP); found {
		if mac, ok := cachedMAC.(string); ok {
			logutil.Logger.Debugf("Using cached MAC for IP %s: %s", clientIP, mac)
			return mac
		}
	}

	// Cache miss - fetch MAC address
	mac := util.GetMACFromARP(clientIP)

	// Cache the result (even if empty) with TTL
	macCache.Set(clientIP, mac, macCacheTTL)

	if mac != "" {
		logutil.Logger.Debugf("Cached MAC for IP %s: %s", clientIP, mac)
	} else {
		logutil.Logger.Debugf("No MAC found for IP %s, cached empty result", clientIP)
	}

	return mac
}

func ShouldUsePublicServers(clientIP string) bool {
	if !cfg.EnableClientRouting {
		return false
	}

	if _, exists := PublicOnlyClientsMap.Load(clientIP); exists {
		return true
	}
	mac := getMACWithCache(clientIP)
	if mac != "" {
		_, exists := PublicOnlyClientMACsMap.Load(mac)
		return exists
	}
	return false
}
