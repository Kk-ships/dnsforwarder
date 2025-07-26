package edns

import (
	"net"
	"slices"
	"strings"

	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"

	"github.com/miekg/dns"
)

const (
	// EDNS Client Subnet option code
	EDNS0SUBNET = dns.EDNS0SUBNET
)

// ClientSubnetManager handles EDNS Client Subnet functionality
type ClientSubnetManager struct {
	Enabled   bool
	Scope     int
	ForwardIP bool
}

// NewClientSubnetManager creates a new EDNS Client Subnet manager
func NewClientSubnetManager() *ClientSubnetManager {
	return &ClientSubnetManager{
		Enabled:   config.EnableEDNSClientSubnet,
		Scope:     config.EDNSClientSubnetScope,
		ForwardIP: config.ForwardClientIP,
	}
}

// AddClientSubnet adds EDNS Client Subnet option to DNS query
func (csm *ClientSubnetManager) AddClientSubnet(msg *dns.Msg, clientIP string) {
	if !csm.Enabled || !csm.ForwardIP || clientIP == "" {
		return
	}

	// Parse client IP
	ip := net.ParseIP(clientIP)
	if ip == nil {
		logutil.Logger.Debugf("Invalid client IP for EDNS Client Subnet: %s", clientIP)
		return
	}

	// Create EDNS0 record if it doesn't exist
	if msg.IsEdns0() == nil {
		msg.SetEdns0(csm.GetEDNSSize(), false)
	}

	edns := msg.IsEdns0()
	if edns == nil {
		logutil.Logger.Debug("Failed to create EDNS0 record")
		return
	}

	// Check if EDNS Client Subnet option already exists
	for i := range edns.Option {
		if edns.Option[i].Option() == EDNS0SUBNET {
			// Option already exists, skip adding
			logutil.Logger.Debug("EDNS Client Subnet option already exists")
			return
		}
	}

	// Create EDNS Client Subnet option
	var subnet *dns.EDNS0_SUBNET
	if ip.To4() != nil {
		// IPv4
		subnet = &dns.EDNS0_SUBNET{
			Code:          EDNS0SUBNET,
			Family:        1, // IPv4
			SourceNetmask: uint8(csm.Scope),
			SourceScope:   0,
			Address:       ip.To4(),
		}
		logutil.Logger.Debugf("Added EDNS Client Subnet for IPv4: %s/%d", clientIP, csm.Scope)
	} else {
		// IPv6
		ipv6Scope := 64 // Default IPv6 scope
		if csm.Scope > 0 {
			ipv6Scope = csm.Scope
		}
		subnet = &dns.EDNS0_SUBNET{
			Code:          EDNS0SUBNET,
			Family:        2, // IPv6
			SourceNetmask: uint8(ipv6Scope),
			SourceScope:   0,
			Address:       ip.To16(),
		}
		logutil.Logger.Debugf("Added EDNS Client Subnet for IPv6: %s/%d", clientIP, ipv6Scope)
	}

	// Add the option to EDNS
	edns.Option = append(edns.Option, subnet)
}

// ProcessClientSubnetResponse processes EDNS Client Subnet in the response
func (csm *ClientSubnetManager) ProcessClientSubnetResponse(response *dns.Msg) {
	if !csm.Enabled || response == nil {
		return
	}

	edns := response.IsEdns0()
	if edns == nil {
		return
	}

	// Look for EDNS Client Subnet option in response
	for _, opt := range edns.Option {
		if subnet, ok := opt.(*dns.EDNS0_SUBNET); ok && subnet.Code == EDNS0SUBNET {
			logutil.Logger.Debugf("Received EDNS Client Subnet response: Family=%d, SourceScope=%d",
				subnet.Family, subnet.SourceScope)

			// Log the scope returned by the upstream server
			// TODO: Add metrics here to track EDNS Client Subnet usage when metrics are implemented
		}
	}
}

// ExtractClientIP extracts the real client IP from various sources
func (csm *ClientSubnetManager) ExtractClientIP(w dns.ResponseWriter) string {
	// Get the remote address
	remoteAddr := w.RemoteAddr()
	if remoteAddr == nil {
		return ""
	}

	// Extract IP from address (remove port)
	addrStr := remoteAddr.String()
	if strings.Contains(addrStr, ":") {
		// Handle both IPv4 and IPv6 addresses
		host, _, err := net.SplitHostPort(addrStr)
		if err == nil {
			return host
		}
	}

	return addrStr
}

// ShouldAddClientSubnet determines if we should add client subnet based on upstream server
func (csm *ClientSubnetManager) ShouldAddClientSubnet(upstreamServer string) bool {
	if !csm.Enabled {
		logutil.Logger.Debugf("EDNS Client Subnet disabled, skipping for server %s", upstreamServer)
		return false
	}

	// Extract server IP from address (remove port if present)
	serverIP := strings.Split(upstreamServer, ":")[0]

	// Check if the upstream server is explicitly in the supported servers list
	supportedProviders := config.EDNSupportedServers
	if len(supportedProviders) == 0 {
		supportedProviders = config.EDNS_SUPPORTED_SERVERS // Fallback to default list
	}

	if slices.Contains(supportedProviders, serverIP) {
		logutil.Logger.Debugf("Server %s is in supported providers list, enabling EDNS Client Subnet", upstreamServer)
		return true
	}

	// For servers not in the explicit list, check if they're private
	ip := net.ParseIP(serverIP)
	if ip != nil {
		isPrivate := isPrivateIP(ip)
		logutil.Logger.Debugf("Server %s: IP=%s, isPrivate=%v, will add EDNS Client Subnet=%v", upstreamServer, serverIP, isPrivate, !isPrivate)
		// Only add EDNS Client Subnet to public (non-private) IP addresses
		// that are not explicitly in our supported list
		return !isPrivate
	}

	logutil.Logger.Debugf("Could not parse IP for server %s, skipping EDNS Client Subnet", upstreamServer)
	return false
}

// isPrivateIP checks if an IP address is private/internal
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	// Check for private IPv4 ranges
	if ip.To4() != nil {
		// 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16
		return ip.IsPrivate()
	}

	// Check for private IPv6 ranges
	// Unique local addresses (fc00::/7)
	if len(ip) == 16 && (ip[0]&0xfe) == 0xfc {
		return true
	}

	return false
}

// GetEDNSSize returns the optimal EDNS buffer size
func (csm *ClientSubnetManager) GetEDNSSize() uint16 {
	if csm.Enabled {
		return 4096 // Larger buffer for EDNS Client Subnet
	}
	return 1232 // Standard size
}

// StripClientSubnet removes EDNS Client Subnet option from response before sending to client
func (csm *ClientSubnetManager) StripClientSubnet(response *dns.Msg) {
	if response == nil {
		return
	}

	edns := response.IsEdns0()
	if edns == nil {
		return
	}

	// Remove EDNS Client Subnet option from response
	var newOptions []dns.EDNS0
	for _, opt := range edns.Option {
		if opt.Option() != EDNS0SUBNET {
			newOptions = append(newOptions, opt)
		}
	}
	edns.Option = newOptions

	logutil.Logger.Debug("Stripped EDNS Client Subnet from response")
}
