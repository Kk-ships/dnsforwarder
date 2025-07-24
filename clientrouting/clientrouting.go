package clientrouting

import (
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/util"
	"strings"
	"sync"
)

var (
	PrivateServersSet        map[string]struct{}
	PublicServersSet         map[string]struct{}
	PrivateAndPublicFallback []string
	PublicServersFallback    []string
	PublicOnlyClientsMap     sync.Map // map[string]bool for fast lookup (IP)
	PublicOnlyClientMACsMap  sync.Map // map[string]bool for fast lookup (MAC)
	PrivateServersCache      []string
	PublicServersCache       []string
)

func InitializeClientRouting() {
	if !config.EnableClientRouting {
		return
	}
	logutil.Logger.Info("Client-based DNS routing enabled")

	PrivateServersSet = make(map[string]struct{})
	PublicServersSet = make(map[string]struct{})

	addServersToSet(config.PrivateServers, PrivateServersSet)
	addServersToSet(config.PublicServers, PublicServersSet)

	PrivateAndPublicFallback = append(config.PrivateServers, config.PublicServers...)
	PublicServersFallback = config.PublicServers[:] // Create a copy

	storeClientsToMap(config.PublicOnlyClients, &PublicOnlyClientsMap, "IP")
	storeMACsToMap(config.PublicOnlyClientMACs, &PublicOnlyClientMACsMap)

	logutil.Logger.Infof("Private servers: %v", config.PrivateServers)
	logutil.Logger.Infof("Public servers: %v", config.PublicServers)
}

func addServersToSet(servers []string, set map[string]struct{}) {
	for _, s := range servers {
		if s != "" {
			set[s] = struct{}{}
		}
	}
}

func storeClientsToMap(clients []string, m *sync.Map, clientType string) {
	for _, client := range clients {
		if client != "" {
			client = strings.TrimSpace(client)
			m.Store(client, true)
			logutil.Logger.Infof("Configured client %s to use public servers only (%s)", client, clientType)
		}
	}
}

func storeMACsToMap(macs []string, m *sync.Map) {
	for _, mac := range macs {
		macNorm := util.NormalizeMAC(mac)
		if macNorm != "" {
			m.Store(macNorm, true)
			logutil.Logger.Infof("Configured client %s to use public servers only (MAC)", macNorm)
		}
	}
}

func ShouldUsePublicServers(clientIP string) bool {
	if !config.EnableClientRouting {
		return false
	}

	if _, exists := PublicOnlyClientsMap.Load(clientIP); exists {
		return true
	}

	mac := util.GetMACFromARP(clientIP)
	if mac != "" {
		_, exists := PublicOnlyClientMACsMap.Load(mac)
		return exists
	}
	return false
}

func GetServersForClient(clientIP string, cacheMutex *sync.RWMutex) []string {
	if !config.EnableClientRouting {
		return nil
	}

	if ShouldUsePublicServers(clientIP) {
		cacheMutex.RLock()
		servers := PublicServersCache
		cacheMutex.RUnlock()
		if len(servers) > 0 {
			return servers
		}
		return PublicServersFallback
	}

	cacheMutex.RLock()
	defer cacheMutex.RUnlock()

	if len(PrivateServersCache) == 0 && len(PublicServersCache) == 0 {
		return PrivateAndPublicFallback
	}

	servers := make([]string, 0, len(PrivateServersCache)+len(PublicServersCache))
	servers = append(servers, PrivateServersCache...)
	servers = append(servers, PublicServersCache...)
	return servers
}

func IsPrivateServer(server string) bool {
	if !config.EnableClientRouting {
		return false
	}
	_, exists := PrivateServersSet[server]
	return exists
}

func IsPublicServer(server string) bool {
	if !config.EnableClientRouting {
		return false
	}
	_, exists := PublicServersSet[server]
	return exists
}
