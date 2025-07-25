package network

import (
	"net"
	"testing"
)

func TestInterfaceManager(t *testing.T) {
	im := NewInterfaceManager()

	// Test listing available interfaces
	t.Run("ListAvailableInterfaces", func(t *testing.T) {
		im.ListAvailableInterfaces()
	})

	// Test interface validation (should not error with default config)
	t.Run("ValidateInterfaces", func(t *testing.T) {
		err := im.ValidateInterfaces()
		if err != nil {
			t.Errorf("ValidateInterfaces failed: %v", err)
		}
	})

	// Test getting listen addresses with default config
	t.Run("GetListenAddresses", func(t *testing.T) {
		addresses, err := im.GetListenAddresses()
		if err != nil {
			t.Errorf("GetListenAddresses failed: %v", err)
		}
		if len(addresses) == 0 {
			t.Error("No listen addresses returned")
		}
		t.Logf("Listen addresses: %v", addresses)
	})

	// Test outbound dialer creation
	t.Run("CreateOutboundDialer", func(t *testing.T) {
		dialer, err := im.CreateOutboundDialer()
		if err != nil {
			t.Errorf("CreateOutboundDialer failed: %v", err)
		}
		if dialer == nil {
			t.Error("Dialer is nil")
		}
	})
}

func TestSocketBinding(t *testing.T) {
	// This test requires root privileges and available interfaces
	// Skip in normal CI/CD environments
	if testing.Short() {
		t.Skip("Skipping socket binding test in short mode")
	}

	im := NewInterfaceManager()

	// Get available interfaces
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("Failed to get interfaces: %v", err)
	}

	var testInterface *net.Interface
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			testInterface = &iface
			break
		}
	}

	if testInterface == nil {
		t.Skip("No suitable test interface found")
	}

	t.Run("CreateBoundUDPListener", func(t *testing.T) {
		// Try to create a bound UDP listener on a high port
		conn, err := im.CreateBoundUDPListener("127.0.0.1:0", testInterface.Name)
		if err != nil {
			t.Logf("CreateBoundUDPListener failed (this may be expected without root): %v", err)
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("Failed to close connection: %v", err)
			}
		}()

		// Test that the connection is working
		addr := conn.LocalAddr()
		if addr == nil {
			t.Error("LocalAddr is nil")
		}
		t.Logf("Created bound listener on %s for interface %s", addr.String(), testInterface.Name)
	})
}

func BenchmarkInterfaceOperations(b *testing.B) {
	im := NewInterfaceManager()

	b.Run("GetListenAddresses", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, err := im.GetListenAddresses()
			if err != nil {
				b.Fatalf("GetListenAddresses failed: %v", err)
			}
		}
	})

	b.Run("ValidateInterfaces", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			err := im.ValidateInterfaces()
			if err != nil {
				b.Fatalf("ValidateInterfaces failed: %v", err)
			}
		}
	})
}
