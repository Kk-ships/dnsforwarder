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

	// Object pools to reduce allocations
	stringBuilderPool = sync.Pool{
		New: func() interface{} {
			return &strings.Builder{}
		},
	}
	byteBufferPool = sync.Pool{
		New: func() interface{} {
			return &bytes.Buffer{}
		},
	}
	stringSlicePool = sync.Pool{
		New: func() interface{} {
			slice := make([]string, 0, 8) // Pre-allocate for common case
			return &slice
		},
	}
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

		// Use object pool for string slice
		slicePtr := stringSlicePool.Get().(*[]string)
		defer stringSlicePool.Put(slicePtr)

		slice := (*slicePtr)[:0] // Reset length but keep capacity

		// Optimize: iterate once instead of split then filter
		start := 0
		for i := 0; i <= len(v); i++ {
			if i == len(v) || v[i] == ',' {
				if start < i {
					s := strings.TrimSpace(v[start:i])
					if s != "" {
						slice = append(slice, s)
					}
				}
				start = i + 1
			}
		}

		// Return a copy to avoid pool pollution
		result := make([]string, len(slice))
		copy(result, slice)
		return result
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

// ClearEnvCaches clears all environment variable caches - for testing only
func ClearEnvCaches() {
	envDurationCache = sync.Map{}
	envIntCache = sync.Map{}
	envStringCache = sync.Map{}
}

// RunCommand runs a system command and returns its output as a string with optimized allocation.
func RunCommand(cmd string, args []string) (string, error) {
	c := exec.Command(cmd, args...)

	// Use pooled buffer to reduce allocations
	buf := byteBufferPool.Get().(*bytes.Buffer)
	defer byteBufferPool.Put(buf)
	buf.Reset()

	c.Stdout = buf
	c.Stderr = buf
	err := c.Run()
	return buf.String(), err
}

// NormalizeMAC returns a lower-case, colon-separated MAC address string with optimized allocation
func NormalizeMAC(mac string) string {
	if mac == "" {
		return ""
	}

	// Use pooled string builder to reduce allocations
	sb := stringBuilderPool.Get().(*strings.Builder)
	defer stringBuilderPool.Put(sb)
	sb.Reset()

	// Pre-allocate capacity for common MAC address length (17 chars: "00:11:22:33:44:55")
	sb.Grow(17)

	// Single pass normalization - convert to lowercase and standardize separators
	var hasContent bool
	for i := 0; i < len(mac); i++ {
		c := mac[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
			sb.WriteByte(c)
			hasContent = true
		case c >= 'A' && c <= 'F':
			sb.WriteByte(c + 32) // Convert to lowercase
			hasContent = true
		case (c == '-' || c == '.' || c == ':' || c == ' ') && hasContent:
			// Add colon separator only if we have content and the last char isn't already a separator
			if sb.Len() > 0 && sb.String()[sb.Len()-1] != ':' {
				sb.WriteByte(':')
			}
		}
	}

	result := sb.String()
	// Remove trailing colon if present
	if len(result) > 0 && result[len(result)-1] == ':' {
		result = result[:len(result)-1]
	}

	return result
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
