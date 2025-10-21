package cache

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAccessTracker(t *testing.T) {
	tracker := NewAccessTracker()
	assert.NotNil(t, tracker)
	assert.NotNil(t, &tracker.accessMap)
}

func TestTrackAccess_NewEntry(t *testing.T) {
	tracker := NewAccessTracker()
	key := "example.com:1"
	domain := "example.com"
	qtype := uint16(1)

	tracker.TrackAccess(key, domain, qtype)

	info, found := tracker.GetAccessInfo(key)
	require.True(t, found)
	assert.Equal(t, int64(1), atomic.LoadInt64(&info.AccessCount))
	assert.Equal(t, domain, info.Domain)
	assert.Equal(t, qtype, info.QueryType)
	assert.False(t, info.FirstAccess.IsZero())
	assert.False(t, info.LastAccess.IsZero())
}

func TestTrackAccess_ExistingEntry(t *testing.T) {
	tracker := NewAccessTracker()
	key := "example.com:1"
	domain := "example.com"
	qtype := uint16(1)

	// First access
	tracker.TrackAccess(key, domain, qtype)
	info1, found := tracker.GetAccessInfo(key)
	require.True(t, found)
	firstAccess := info1.FirstAccess

	// Small delay to ensure LastAccess changes
	time.Sleep(10 * time.Millisecond)

	// Second access
	tracker.TrackAccess(key, domain, qtype)
	info2, found := tracker.GetAccessInfo(key)
	require.True(t, found)

	assert.Equal(t, int64(2), atomic.LoadInt64(&info2.AccessCount))
	assert.Equal(t, firstAccess, info2.FirstAccess)     // FirstAccess should not change
	assert.True(t, info2.LastAccess.After(firstAccess)) // LastAccess should be updated
}

func TestTrackAccess_Concurrent(t *testing.T) {
	tracker := NewAccessTracker()
	key := "example.com:1"
	domain := "example.com"
	qtype := uint16(1)
	numGoroutines := 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			tracker.TrackAccess(key, domain, qtype)
		}()
	}

	wg.Wait()

	info, found := tracker.GetAccessInfo(key)
	require.True(t, found)
	assert.Equal(t, int64(numGoroutines), atomic.LoadInt64(&info.AccessCount))
}

func TestGetAccessInfo_NotFound(t *testing.T) {
	tracker := NewAccessTracker()
	info, found := tracker.GetAccessInfo("nonexistent")
	assert.False(t, found)
	assert.Nil(t, info)
}

func TestGetFrequentlyAccessed(t *testing.T) {
	tracker := NewAccessTracker()

	// Create entries with different access counts
	entries := map[string]int64{
		"frequent1.com:1":   10,
		"frequent2.com:1":   15,
		"infrequent1.com:1": 2,
		"infrequent2.com:1": 3,
		"frequent3.com:1":   20,
	}

	for key, count := range entries {
		for i := int64(0); i < count; i++ {
			tracker.TrackAccess(key, "domain", 1)
		}
	}

	// Get entries with at least 10 accesses
	frequentKeys := tracker.GetFrequentlyAccessed(10)
	assert.Len(t, frequentKeys, 3)
	assert.Contains(t, frequentKeys, "frequent1.com:1")
	assert.Contains(t, frequentKeys, "frequent2.com:1")
	assert.Contains(t, frequentKeys, "frequent3.com:1")
}

func TestGetFrequentlyAccessed_Empty(t *testing.T) {
	tracker := NewAccessTracker()
	frequentKeys := tracker.GetFrequentlyAccessed(10)
	assert.Empty(t, frequentKeys)
}

func TestCleanupExpiredEntries(t *testing.T) {
	// Initialize a temporary cache for testing
	oldCache := DnsCache
	defer func() { DnsCache = oldCache }()
	DnsCache = cache.New(5*time.Minute, 10*time.Minute)

	tracker := NewAccessTracker()

	// Add some entries to both cache and tracker
	key1 := "cached.com:1"
	key2 := "notcached.com:1"
	key3 := "alsocached.com:1"

	tracker.TrackAccess(key1, "cached.com", 1)
	tracker.TrackAccess(key2, "notcached.com", 1)
	tracker.TrackAccess(key3, "alsocached.com", 1)

	// Add only some keys to the actual cache
	DnsCache.Set(key1, []string{"answer1"}, cache.DefaultExpiration)
	DnsCache.Set(key3, []string{"answer3"}, cache.DefaultExpiration)

	// Verify all three are in tracker
	_, found := tracker.GetAccessInfo(key1)
	assert.True(t, found)
	_, found = tracker.GetAccessInfo(key2)
	assert.True(t, found)
	_, found = tracker.GetAccessInfo(key3)
	assert.True(t, found)

	// Cleanup expired entries
	tracker.CleanupExpiredEntries()

	// Verify only cached entries remain in tracker
	_, found = tracker.GetAccessInfo(key1)
	assert.True(t, found, "key1 should still be in tracker")
	_, found = tracker.GetAccessInfo(key2)
	assert.False(t, found, "key2 should be removed from tracker")
	_, found = tracker.GetAccessInfo(key3)
	assert.True(t, found, "key3 should still be in tracker")
}

func TestCleanupExpiredEntries_NilCache(t *testing.T) {
	oldCache := DnsCache
	defer func() { DnsCache = oldCache }()
	DnsCache = nil

	tracker := NewAccessTracker()
	tracker.TrackAccess("test.com:1", "test.com", 1)

	// Should not panic with nil cache
	assert.NotPanics(t, func() {
		tracker.CleanupExpiredEntries()
	})
}

func BenchmarkTrackAccess(b *testing.B) {
	tracker := NewAccessTracker()
	key := "example.com:1"
	domain := "example.com"
	qtype := uint16(1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tracker.TrackAccess(key, domain, qtype)
	}
}

func BenchmarkGetAccessInfo(b *testing.B) {
	tracker := NewAccessTracker()
	key := "example.com:1"
	tracker.TrackAccess(key, "example.com", 1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tracker.GetAccessInfo(key)
	}
}

func BenchmarkGetFrequentlyAccessed(b *testing.B) {
	tracker := NewAccessTracker()

	// Populate with 1000 entries
	for i := 0; i < 1000; i++ {
		key := "example" + string(rune(i)) + ".com:1"
		for j := 0; j < i%50; j++ {
			tracker.TrackAccess(key, "domain", 1)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tracker.GetFrequentlyAccessed(10)
	}
}
