package util

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

var (
	envDurationCache sync.Map // map[string]time.Duration
	envIntCache      sync.Map // map[string]int
	envStringCache   sync.Map // map[string]string
)

func GetEnvDuration(key string, def time.Duration) time.Duration {
	if v, ok := envDurationCache.Load(key); ok {
		return v.(time.Duration)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			val = d
		}
	}
	envDurationCache.Store(key, val)
	return val
}

func GetEnvInt(key string, def int) int {
	if v, ok := envIntCache.Load(key); ok {
		return v.(int)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		if i, err := strconv.Atoi(s); err == nil {
			val = i
		}
	}
	envIntCache.Store(key, val)
	return val
}

func GetEnvString(key, def string) string {
	if v, ok := envStringCache.Load(key); ok {
		return v.(string)
	}
	val := def
	if s := os.Getenv(key); s != "" {
		val = s
	}
	envStringCache.Store(key, val)
	return val
}

func GetEnvStringSlice(key, def string) []string {
	if v := os.Getenv(key); v != "" {
		if !strings.Contains(v, ",") {
			return []string{strings.TrimSpace(v)}
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for i := range parts {
			s := strings.TrimSpace(parts[i])
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	if def == "" {
		return []string{} // return empty slice if no default is provided
	}
	return []string{def}
}

func GetEnvBool(key string, def bool) bool {
	if v, ok := envStringCache.Load(key); ok {
		return v.(string) == "true"
	}
	val := def
	if s := os.Getenv(key); s != "" {
		switch s {
		case "true", "1":
			val = true
		case "false", "0":
			val = false
		}
	}
	envStringCache.Store(key, strconv.FormatBool(val))
	return val
}

// RunCommand runs a system command and returns its output as a string.
func RunCommand(cmd string, args []string) (string, error) {
	c := exec.Command(cmd, args...)
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out
	err := c.Run()
	return out.String(), err
}

// NormalizeMAC returns a lower-case, colon-separated MAC address string
func NormalizeMAC(mac string) string {
	mac = strings.ToLower(strings.ReplaceAll(mac, "-", ":"))
	mac = strings.ReplaceAll(mac, ".", ":")
	mac = strings.ReplaceAll(mac, " ", "")
	// Remove duplicate colons
	parts := strings.Split(mac, ":")
	var filtered []string
	for _, p := range parts {
		if p != "" {
			filtered = append(filtered, p)
		}
	}
	return strings.Join(filtered, ":")
}

// GetMACFromARP tries to resolve the MAC address for a given IP using the system ARP table
func GetMACFromARP(ip string) string {
	// Only works on Unix-like systems
	out, err := os.ReadFile("/proc/net/arp")
	if err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) >= 4 && fields[0] == ip {
				return NormalizeMAC(fields[3])
			}
		}
	}
	// Fallback: use arp command (macOS, Linux)
	arpOut, err := RunCommand("arp", []string{"-n", ip})
	if err == nil {
		// Output: ? (192.168.1.100) at 00:11:22:33:44:55 on en0 ifscope [ethernet]
		parts := strings.Split(arpOut, " at ")
		if len(parts) > 1 {
			macPart := strings.Split(parts[1], " ")[0]
			return NormalizeMAC(macPart)
		}
	}
	return ""
}

func GetClientIP(w dns.ResponseWriter) string {
	addr := w.RemoteAddr()
	switch a := addr.(type) {
	case *net.UDPAddr:
		return a.IP.String()
	case *net.TCPAddr:
		return a.IP.String()
	default:
		s := addr.String()
		if i := strings.LastIndex(s, ":"); i > 0 {
			return s[:i]
		}
		return s
	}
}
