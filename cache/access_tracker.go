package cache

import (
	"sync"
	"sync/atomic"
	"time"
)

// AccessInfo tracks access patterns for individual cache entries
type AccessInfo struct {
	AccessCount      int64  `json:"access_count"`
	LastAccessNanos  int64  `json:"last_access_nanos"`  // Unix nanoseconds for atomic access
	FirstAccessNanos int64  `json:"first_access_nanos"` // Unix nanoseconds for atomic access
	Domain           string `json:"domain"`
	QueryType        uint16 `json:"query_type"`
}

// LastAccess returns the last access time (non-blocking read)
func (ai *AccessInfo) LastAccess() time.Time {
	nanos := atomic.LoadInt64(&ai.LastAccessNanos)
	return time.Unix(0, nanos)
}

// FirstAccess returns the first access time (non-blocking read)
func (ai *AccessInfo) FirstAccess() time.Time {
	nanos := atomic.LoadInt64(&ai.FirstAccessNanos)
	return time.Unix(0, nanos)
}

// AccessTracker manages access tracking for cache entries
type AccessTracker struct {
	accessMap sync.Map // map[string]*AccessInfo
}

// NewAccessTracker creates a new access tracker
func NewAccessTracker() *AccessTracker {
	return &AccessTracker{}
}

// TrackAccess records an access to a cache entry (non-blocking)
func (at *AccessTracker) TrackAccess(key, domain string, qtype uint16) {
	now := time.Now()
	nowNanos := now.UnixNano()

	// Try to load existing access info
	if value, ok := at.accessMap.Load(key); ok {
		if accessInfo, ok := value.(*AccessInfo); ok {
			atomic.AddInt64(&accessInfo.AccessCount, 1)
			atomic.StoreInt64(&accessInfo.LastAccessNanos, nowNanos)
			return
		}
	}

	// Create new access info
	accessInfo := &AccessInfo{
		AccessCount:      1,
		LastAccessNanos:  nowNanos,
		FirstAccessNanos: nowNanos,
		Domain:           domain,
		QueryType:        qtype,
	}
	at.accessMap.Store(key, accessInfo)
}

// GetAccessInfo returns access information for a key
func (at *AccessTracker) GetAccessInfo(key string) (*AccessInfo, bool) {
	if value, ok := at.accessMap.Load(key); ok {
		if accessInfo, ok := value.(*AccessInfo); ok {
			return accessInfo, true
		}
	}
	return nil, false
}

// GetFrequentlyAccessed returns keys that are frequently accessed
func (at *AccessTracker) GetFrequentlyAccessed(minCount int64) []string {
	var keys []string
	at.accessMap.Range(func(key, value any) bool {
		if accessInfo, ok := value.(*AccessInfo); ok {
			if atomic.LoadInt64(&accessInfo.AccessCount) >= minCount {
				keys = append(keys, key.(string))
			}
		}
		return true
	})
	return keys
}

// CleanupExpiredEntries removes access tracking for entries that are no longer in cache
func (at *AccessTracker) CleanupExpiredEntries() {
	if DnsCache == nil {
		return
	}

	cacheItems := DnsCache.Items()
	at.accessMap.Range(func(key, value any) bool {
		keyStr := key.(string)
		if _, exists := cacheItems[keyStr]; !exists {
			at.accessMap.Delete(keyStr)
		}
		return true
	})
}
