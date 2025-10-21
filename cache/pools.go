package cache

import (
	"strings"
	"sync"

	"github.com/miekg/dns"
)

// Pre-allocated buffer pool for cache keys to reduce allocations
var keyBuilderPool = sync.Pool{
	New: func() any {
		builder := &strings.Builder{}
		builder.Grow(128) // Pre-allocate reasonable capacity for most domain names
		return builder
	},
}

// Pool for DNS RR slices to reduce allocations
var dnsRRSlicePool = sync.Pool{
	New: func() any {
		slice := make([]dns.RR, 0, 16) // Pre-allocate common slice size
		return &slice
	},
}

// Pool for string slices used in JSON serialization
var stringSlicePool = sync.Pool{
	New: func() any {
		slice := make([]string, 0, 16)
		return &slice
	},
}

// getStringSlice gets a slice from the pool and resets it
func getStringSlice() []string {
	slicePtr := stringSlicePool.Get().(*[]string)
	slice := *slicePtr
	return slice[:0] // Reset length but keep capacity
}

// putStringSlice returns a slice to the pool if it's not too large
func putStringSlice(slice []string) {
	const maxPoolSliceSize = 256
	if cap(slice) <= maxPoolSliceSize {
		stringSlicePool.Put(&slice)
	}
}

// getDNSRRSlice gets a slice from the pool and resets it
func getDNSRRSlice() []dns.RR {
	slicePtr := dnsRRSlicePool.Get().(*[]dns.RR)
	slice := *slicePtr
	return slice[:0] // Reset length but keep capacity
}

// putDNSRRSlice returns a slice to the pool if it's not too large
func putDNSRRSlice(slice []dns.RR) {
	const maxPoolSliceSize = 256
	if cap(slice) <= maxPoolSliceSize {
		dnsRRSlicePool.Put(&slice)
	}
}
