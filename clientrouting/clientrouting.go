package clientrouting

import (
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/util"
	"strings"
	"sync"
)

var (
	PublicOnlyClientsMap    sync.Map // map[string]bool for fast lookup (IP)
	PublicOnlyClientMACsMap sync.Map // map[string]bool for fast lookup (MAC)
)

func InitializeClientRouting() {
	if !config.EnableClientRouting {
		return
	}
	logutil.Logger.Info("Client-based DNS routing enabled")
	storeClientsToMap(config.PublicOnlyClients, &PublicOnlyClientsMap, "IP")
	storeMACsToMap(config.PublicOnlyClientMACs, &PublicOnlyClientMACsMap)
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
