package cache

import (
	"sync"
	"sync/atomic"
	"time"
)

// AccessInfo tracks access patterns for individual cache entries
type AccessInfo struct {
	AccessCount int64     `json:"access_count"`
	LastAccess  time.Time `json:"last_access"`
	FirstAccess time.Time `json:"first_access"`
	Domain      string    `json:"domain"`
	QueryType   uint16    `json:"query_type"`
}

// AccessTracker manages access tracking for cache entries
type AccessTracker struct {
	accessMap sync.Map // map[string]*AccessInfo
	mu        sync.RWMutex
}

// NewAccessTracker creates a new access tracker
func NewAccessTracker() *AccessTracker {
	return &AccessTracker{}
}

// TrackAccess records an access to a cache entry
func (at *AccessTracker) TrackAccess(key, domain string, qtype uint16) {
	now := time.Now()

	// Try to load existing access info
	if value, ok := at.accessMap.Load(key); ok {
		if accessInfo, ok := value.(*AccessInfo); ok {
			atomic.AddInt64(&accessInfo.AccessCount, 1)
			at.mu.Lock()
			accessInfo.LastAccess = now
			at.mu.Unlock()
			return
		}
	}

	// Create new access info
	accessInfo := &AccessInfo{
		AccessCount: 1,
		LastAccess:  now,
		FirstAccess: now,
		Domain:      domain,
		QueryType:   qtype,
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
