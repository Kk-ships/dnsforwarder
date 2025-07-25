package network

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
)

// InterfaceManager handles network interface binding and routing
type InterfaceManager struct {
	ListenInterfaces  []string
	OutboundInterface string
	BindToInterface   bool
}

// NewInterfaceManager creates a new interface manager
func NewInterfaceManager() *InterfaceManager {
	return &InterfaceManager{
		ListenInterfaces:  config.ListenInterfaces,
		OutboundInterface: config.OutboundInterface,
		BindToInterface:   config.BindToInterface,
	}
}

// GetListenAddresses returns the addresses to listen on based on interface configuration
func (im *InterfaceManager) GetListenAddresses() ([]string, error) {
	var addresses []string

	if len(im.ListenInterfaces) == 0 || !im.BindToInterface {
		// Use default listen address if no specific interfaces configured
		addresses = append(addresses, config.DefaultListenAddress)
		logutil.Logger.Infof("Using default listen address: %s", config.DefaultListenAddress)
		return addresses, nil
	}

	// Get addresses for specified interfaces
	for _, ifaceName := range im.ListenInterfaces {
		ifaceAddrs, err := im.getInterfaceAddresses(ifaceName)
		if err != nil {
			logutil.Logger.Errorf("Failed to get addresses for interface %s: %v", ifaceName, err)
			continue
		}
		addresses = append(addresses, ifaceAddrs...)
	}

	if len(addresses) == 0 {
		return nil, fmt.Errorf("no valid addresses found for specified interfaces: %v", im.ListenInterfaces)
	}

	logutil.Logger.Infof("Listening on interfaces %v with addresses: %v", im.ListenInterfaces, addresses)
	return addresses, nil
}

// getInterfaceAddresses returns the IP addresses for a given interface
func (im *InterfaceManager) getInterfaceAddresses(ifaceName string) ([]string, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %v", ifaceName, err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("failed to get addresses for interface %s: %v", ifaceName, err)
	}

	var addresses []string
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			// Extract port from config.DefaultDNSPort or use :53
			port := ":53"
			if strings.Contains(config.DefaultDNSPort, ":") {
				port = config.DefaultDNSPort[strings.LastIndex(config.DefaultDNSPort, ":"):]
			}

			if ipnet.IP.To4() != nil {
				// IPv4
				addresses = append(addresses, ipnet.IP.String()+port)
			} else {
				// IPv6
				addresses = append(addresses, "["+ipnet.IP.String()+"]"+port)
			}
		}
	}

	return addresses, nil
}

// CreateOutboundDialer creates a dialer that binds to the specified outbound interface
func (im *InterfaceManager) CreateOutboundDialer() (*net.Dialer, error) {
	dialer := &net.Dialer{}

	if im.OutboundInterface == "" || !im.BindToInterface {
		return dialer, nil
	}

	// Get the outbound interface
	iface, err := net.InterfaceByName(im.OutboundInterface)
	if err != nil {
		return nil, fmt.Errorf("outbound interface %s not found: %v", im.OutboundInterface, err)
	}

	// Get the first usable address from the interface
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("failed to get addresses for outbound interface %s: %v", im.OutboundInterface, err)
	}

	var localAddr net.Addr
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				localAddr = &net.UDPAddr{IP: ipnet.IP, Port: 0}
				break
			}
		}
	}

	if localAddr == nil {
		return nil, fmt.Errorf("no usable address found on outbound interface %s", im.OutboundInterface)
	}

	dialer.LocalAddr = localAddr
	logutil.Logger.Infof("Configured outbound interface %s with address %s", im.OutboundInterface, localAddr.String())

	return dialer, nil
}

// ListAvailableInterfaces lists all available network interfaces
func (im *InterfaceManager) ListAvailableInterfaces() {
	interfaces, err := net.Interfaces()
	if err != nil {
		logutil.Logger.Errorf("Failed to list network interfaces: %v", err)
		return
	}

	logutil.Logger.Info("Available network interfaces:")
	interfaceChan := make(chan net.Interface, len(interfaces))
	for _, iface := range interfaces {
		interfaceChan <- iface
	}
	close(interfaceChan)

	var wg sync.WaitGroup
	numWorkers := runtime.NumCPU() // Adjust the number of workers as needed
	wg.Add(numWorkers)

	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()
			for iface := range interfaceChan {
				if iface.Flags&net.FlagUp == 0 {
					continue // Skip down interfaces
				}

				addrs, err := iface.Addrs()
				if err != nil {
					logutil.Logger.Errorf("Failed to get addresses for interface %s: %v", iface.Name, err)
					continue
				}

				var addresses []string
				for _, addr := range addrs {
					addresses = append(addresses, addr.String())
				}

				logutil.Logger.Infof("  %s: %v (Flags: %v)", iface.Name, addresses, iface.Flags)
			}
		}()
	}

	wg.Wait()
}

// ValidateInterfaces validates that the configured interfaces exist and are usable
func (im *InterfaceManager) ValidateInterfaces() error {
	if !im.BindToInterface {
		return nil
	}

	// Validate listen interfaces in parallel
	errChan := make(chan error, len(im.ListenInterfaces))
	for _, ifaceName := range im.ListenInterfaces {
		go func(ifaceName string) {
			iface, err := net.InterfaceByName(ifaceName)
			if err != nil {
				errChan <- fmt.Errorf("listen interface %s not found: %v", ifaceName, err)
				return
			}
			if iface.Flags&net.FlagUp == 0 {
				errChan <- fmt.Errorf("listen interface %s is down", ifaceName)
				return
			}
			errChan <- nil // Signal success
		}(ifaceName)
	}

	// Collect errors from goroutines
	for i := 0; i < len(im.ListenInterfaces); i++ {
		if err := <-errChan; err != nil {
			return err // Return immediately on first error
		}
	}

	// Validate outbound interface
	if im.OutboundInterface != "" {
		iface, err := net.InterfaceByName(im.OutboundInterface)
		if err != nil {
			return fmt.Errorf("outbound interface %s not found: %v", im.OutboundInterface, err)
		}
		if iface.Flags&net.FlagUp == 0 {
			return fmt.Errorf("outbound interface %s is down", im.OutboundInterface)
		}
	}

	return nil
}

// SetSocketOptions sets socket options for interface binding (platform-specific)
func (im *InterfaceManager) SetSocketOptions(fd int, interfaceName string) error {
	if !im.BindToInterface || interfaceName == "" {
		return nil
	}

	switch runtime.GOOS {
	case "linux":
		return im.setLinuxSocketOptions(fd, interfaceName)
	case "darwin":
		return im.setDarwinSocketOptions(fd, interfaceName)
	case "windows":
		return im.setWindowsSocketOptions(fd, interfaceName)
	default:
		logutil.Logger.Warnf("Socket interface binding not implemented for platform %s (interface: %s)", runtime.GOOS, interfaceName)
		return nil
	}
}

// setLinuxSocketOptions sets socket options for Linux (SO_BINDTODEVICE)
func (im *InterfaceManager) setLinuxSocketOptions(fd int, interfaceName string) error {
	const SO_BINDTODEVICE = 25 // Linux-specific constant

	// Convert interface name to bytes (null-terminated)
	ifaceBytes := make([]byte, len(interfaceName)+1)
	copy(ifaceBytes, interfaceName)

	err := syscall.SetsockoptString(fd, syscall.SOL_SOCKET, SO_BINDTODEVICE, interfaceName)
	if err != nil {
		return fmt.Errorf("failed to bind socket to interface %s (Linux SO_BINDTODEVICE): %v", interfaceName, err)
	}

	logutil.Logger.Infof("Socket bound to interface %s using SO_BINDTODEVICE", interfaceName)
	return nil
}

// setDarwinSocketOptions sets socket options for macOS/Darwin
func (im *InterfaceManager) setDarwinSocketOptions(fd int, interfaceName string) error {
	// On macOS, we can use IP_BOUND_IF for IPv4 and IPV6_BOUND_IF for IPv6
	// Get interface index
	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return fmt.Errorf("interface %s not found: %v", interfaceName, err)
	}

	// Set IP_BOUND_IF (for IPv4) - constant value 25 on Darwin
	const IP_BOUND_IF = 25
	err = syscall.SetsockoptInt(fd, syscall.IPPROTO_IP, IP_BOUND_IF, iface.Index)
	if err != nil {
		logutil.Logger.Warnf("Failed to set IP_BOUND_IF for interface %s: %v", interfaceName, err)
		// Don't return error, try IPV6_BOUND_IF
	}

	// Set IPV6_BOUND_IF (for IPv6) - constant value 125 on Darwin
	const IPV6_BOUND_IF = 125
	err = syscall.SetsockoptInt(fd, syscall.IPPROTO_IPV6, IPV6_BOUND_IF, iface.Index)
	if err != nil {
		logutil.Logger.Warnf("Failed to set IPV6_BOUND_IF for interface %s: %v", interfaceName, err)
		// If both fail, return error
		return fmt.Errorf("failed to bind socket to interface %s (Darwin): %v", interfaceName, err)
	}

	logutil.Logger.Infof("Socket bound to interface %s using IP_BOUND_IF/IPV6_BOUND_IF", interfaceName)
	return nil
}

// setWindowsSocketOptions sets socket options for Windows
func (im *InterfaceManager) setWindowsSocketOptions(_ int, interfaceName string) error {
	// On Windows, interface binding is more complex and typically requires WinSock2
	// For now, we'll provide a placeholder implementation
	logutil.Logger.Warnf("Socket interface binding not fully implemented for Windows (interface: %s)", interfaceName)
	logutil.Logger.Info("Consider using IP-based binding instead of interface names on Windows")
	return nil
}

// CreateBoundUDPListener creates a UDP listener bound to a specific interface
func (im *InterfaceManager) CreateBoundUDPListener(address string, interfaceName string) (net.PacketConn, error) {
	if !im.BindToInterface || interfaceName == "" {
		// Standard UDP listener without interface binding
		return net.ListenPacket("udp", address)
	}

	// Parse the address
	udpAddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve UDP address %s: %v", address, err)
	}

	// Create socket manually for interface binding
	var domain int
	if udpAddr.IP.To4() != nil {
		domain = syscall.AF_INET
	} else {
		domain = syscall.AF_INET6
	}

	// Create socket
	fd, err := syscall.Socket(domain, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to create socket: %v", err)
	}

	// Set socket options for interface binding
	if err := im.SetSocketOptions(fd, interfaceName); err != nil {
		if closeErr := syscall.Close(fd); closeErr != nil {
			logutil.Logger.Errorf("Failed to close socket: %v", closeErr)
		}
		return nil, fmt.Errorf("failed to set socket options: %v", err)
	}

	// Set SO_REUSEADDR
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		if closeErr := syscall.Close(fd); closeErr != nil {
			logutil.Logger.Errorf("Failed to close socket: %v", closeErr)
		}
		return nil, fmt.Errorf("failed to set SO_REUSEADDR: %v", err)
	}

	// Set SO_REUSEPORT if available (Linux and some BSDs)
	if runtime.GOOS == "linux" {
		const SO_REUSEPORT = 15
		if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, SO_REUSEPORT, 1); err != nil {
			logutil.Logger.Warnf("Failed to set SO_REUSEPORT: %v", err)
		}
	}

	// Bind to address
	var sockaddr syscall.Sockaddr
	if domain == syscall.AF_INET {
		sa := &syscall.SockaddrInet4{Port: udpAddr.Port}
		copy(sa.Addr[:], udpAddr.IP.To4())
		sockaddr = sa
	} else {
		sa := &syscall.SockaddrInet6{Port: udpAddr.Port}
		copy(sa.Addr[:], udpAddr.IP.To16())
		sockaddr = sa
	}

	if err := syscall.Bind(fd, sockaddr); err != nil {
		if closeErr := syscall.Close(fd); closeErr != nil {
			logutil.Logger.Errorf("Failed to close socket: %v", closeErr)
		}
		return nil, fmt.Errorf("failed to bind to address %s: %v", address, err)
	}

	// Convert to net.PacketConn
	file := os.NewFile(uintptr(fd), "udp-socket")
	if file == nil {
		if closeErr := syscall.Close(fd); closeErr != nil {
			logutil.Logger.Errorf("Failed to close socket: %v", closeErr)
		}
		return nil, fmt.Errorf("failed to create file from socket")
	}
	defer func() {
		if err := file.Close(); err != nil {
			logutil.Logger.Errorf("Failed to close file: %v", err)
		}
	}()

	conn, err := net.FilePacketConn(file)
	if err != nil {
		return nil, fmt.Errorf("failed to create PacketConn from file: %v", err)
	}

	logutil.Logger.Infof("Created UDP listener on %s bound to interface %s", address, interfaceName)
	return conn, nil
}

// TestSocketBinding tests if socket binding capabilities are available
func (im *InterfaceManager) TestSocketBinding() error {
	if !im.BindToInterface {
		return nil // Not enabled, no need to test
	}

	// Get a test interface
	interfaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("failed to get network interfaces: %v", err)
	}

	var testInterface *net.Interface
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			testInterface = &iface
			break
		}
	}

	if testInterface == nil {
		return fmt.Errorf("no suitable network interface found for testing")
	}

	// Try to create a test socket and bind it
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("failed to create test socket: %v", err)
	}
	defer func() {
		if err := syscall.Close(fd); err != nil {
			logutil.Logger.Errorf("Failed to close test socket: %v", err)
		}
	}()

	// Test socket binding
	err = im.SetSocketOptions(fd, testInterface.Name)
	if err != nil {
		return fmt.Errorf("socket binding test failed: %v (hint: may require root privileges)", err)
	}

	logutil.Logger.Infof("Socket binding test successful with interface %s", testInterface.Name)
	return nil
}

// GetInterfaceCapabilities returns information about interface binding capabilities
func (im *InterfaceManager) GetInterfaceCapabilities() map[string]interface{} {
	capabilities := map[string]interface{}{
		"platform":           runtime.GOOS,
		"binding_enabled":    im.BindToInterface,
		"listen_interfaces":  im.ListenInterfaces,
		"outbound_interface": im.OutboundInterface,
	}

	// Test if socket binding is available
	testErr := im.TestSocketBinding()
	capabilities["socket_binding_available"] = testErr == nil
	if testErr != nil {
		capabilities["socket_binding_error"] = testErr.Error()
	}

	// Check for root privileges on Unix systems
	if runtime.GOOS != "windows" {
		capabilities["running_as_root"] = syscall.Getuid() == 0
	}

	return capabilities
}
