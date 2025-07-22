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

	PrivateServersSet = make(map[string]struct{})
	PublicServersSet = make(map[string]struct{})
	for _, s := range config.PrivateServers {
		if s != "" {
			PrivateServersSet[s] = struct{}{}
		}
	}
	for _, s := range config.PublicServers {
		if s != "" {
			PublicServersSet[s] = struct{}{}
		}
	}
	PrivateAndPublicFallback = append([]string{}, config.PrivateServers...)
	PrivateAndPublicFallback = append(PrivateAndPublicFallback, config.PublicServers...)
	PublicServersFallback = append([]string{}, config.PublicServers...)

	for _, client := range config.PublicOnlyClients {
		if client != "" {
			PublicOnlyClientsMap.Store(strings.TrimSpace(client), true)
			logutil.LogWithBufferf("Configured client %s to use public servers only (IP)", client)
		}
	}
	for _, mac := range config.PublicOnlyClientMACs {
		macNorm := util.NormalizeMAC(mac)
		if macNorm != "" {
			PublicOnlyClientMACsMap.Store(macNorm, true)
			logutil.LogWithBufferf("Configured client %s to use public servers only (MAC)", macNorm)
		}
	}

	logutil.LogWithBufferf("Private servers: %v", config.PrivateServers)
	logutil.LogWithBufferf("Public servers: %v", config.PublicServers)
	logutil.LogWithBufferf("Client routing enabled: %v", config.EnableClientRouting)
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
		if _, exists := PublicOnlyClientMACsMap.Load(mac); exists {
			return true
		}
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
	priv := PrivateServersCache
	pub := PublicServersCache
	cacheMutex.RUnlock()
	if len(priv) == 0 && len(pub) == 0 {
		return PrivateAndPublicFallback
	}
	if len(pub) == 0 {
		return priv
	}
	if len(priv) == 0 {
		return pub
	}
	servers := make([]string, 0, len(priv)+len(pub))
	servers = append(servers, priv...)
	servers = append(servers, pub...)
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
