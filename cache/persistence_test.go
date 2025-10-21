package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dnsloadbalancer/config"

	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestCache() (*cache.Cache, func()) {
	oldCache := DnsCache
	oldCfg := cfg

	DnsCache = cache.New(5*time.Minute, 10*time.Minute)
	DefaultDNSCacheTTL = 5 * time.Minute

	return DnsCache, func() {
		DnsCache = oldCache
		cfg = oldCfg
	}
}

func TestEnsureCacheDir_Success(t *testing.T) {
	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "test_cache", "cache.json")

	oldCfg := cfg
	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = cacheFile
	defer func() { cfg = oldCfg }()

	err := ensureCacheDir()
	assert.NoError(t, err)

	// Verify directory was created
	dir := filepath.Dir(cacheFile)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestEnsureCacheDir_DisabledPersistence(t *testing.T) {
	oldCfg := cfg
	cfg = config.Get()
	cfg.EnableCachePersistence = false
	defer func() { cfg = oldCfg }()

	err := ensureCacheDir()
	assert.NoError(t, err)
}

func TestSaveCacheToFile_Success(t *testing.T) {
	_, cleanup := setupTestCache()
	defer cleanup()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "cache.json")

	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = cacheFile

	// Add some test data
	rr1, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	rr2, _ := dns.NewRR("test.com. 300 IN A 5.6.7.8")

	DnsCache.Set("example.com:1", []dns.RR{rr1}, 5*time.Minute)
	DnsCache.Set("test.com:1", []dns.RR{rr2}, 5*time.Minute)

	err := SaveCacheToFile()
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(cacheFile)
	assert.NoError(t, err)

	// Verify file contents
	data, err := os.ReadFile(cacheFile)
	require.NoError(t, err)

	var snapshot CacheSnapshot
	err = json.Unmarshal(data, &snapshot)
	require.NoError(t, err)

	assert.Len(t, snapshot.Entries, 2)
	assert.False(t, snapshot.Timestamp.IsZero())
}

func TestSaveCacheToFile_SkipsExpiredEntries(t *testing.T) {
	_, cleanup := setupTestCache()
	defer cleanup()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "cache.json")

	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = cacheFile

	// Add entry that will expire
	rr, _ := dns.NewRR("expired.com. 300 IN A 1.2.3.4")
	DnsCache.Set("expired.com:1", []dns.RR{rr}, 1*time.Millisecond)

	// Wait for expiration
	time.Sleep(10 * time.Millisecond)

	err := SaveCacheToFile()
	require.NoError(t, err)

	// Verify expired entry is not saved
	data, err := os.ReadFile(cacheFile)
	require.NoError(t, err)

	var snapshot CacheSnapshot
	err = json.Unmarshal(data, &snapshot)
	require.NoError(t, err)

	assert.Len(t, snapshot.Entries, 0)
}

func TestSaveCacheToFile_DisabledPersistence(t *testing.T) {
	_, cleanup := setupTestCache()
	defer cleanup()

	cfg = config.Get()
	cfg.EnableCachePersistence = false

	err := SaveCacheToFile()
	assert.NoError(t, err)
}

func TestSaveCacheToFile_NilCache(t *testing.T) {
	oldCache := DnsCache
	defer func() { DnsCache = oldCache }()
	DnsCache = nil

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "cache.json")

	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = cacheFile

	err := SaveCacheToFile()
	assert.NoError(t, err)
}

func TestLoadCacheFromFile_Success(t *testing.T) {
	_, cleanup := setupTestCache()
	defer cleanup()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "cache.json")

	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = cacheFile
	cfg.CachePersistenceMaxAge = 1 * time.Hour

	// Create a test cache file
	snapshot := CacheSnapshot{
		Entries: []CacheEntry{
			{
				Key:        "example.com:1",
				Answers:    []string{"example.com.\t300\tIN\tA\t1.2.3.4"},
				Expiration: time.Now().Add(5 * time.Minute),
			},
		},
		Timestamp: time.Now(),
	}

	data, _ := json.MarshalIndent(snapshot, "", "  ")
	err := os.WriteFile(cacheFile, data, 0644)
	require.NoError(t, err)

	// Load the cache
	err = LoadCacheFromFile()
	require.NoError(t, err)

	// Verify cache was loaded
	val, found := DnsCache.Get("example.com:1")
	assert.True(t, found)
	answers, ok := val.([]dns.RR)
	require.True(t, ok)
	assert.Len(t, answers, 1)
}

func TestLoadCacheFromFile_NoFile(t *testing.T) {
	_, cleanup := setupTestCache()
	defer cleanup()

	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = "/nonexistent/path/cache.json"

	err := LoadCacheFromFile()
	assert.NoError(t, err) // Should not error, just log info
}

func TestLoadCacheFromFile_OldCache(t *testing.T) {
	_, cleanup := setupTestCache()
	defer cleanup()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "cache.json")

	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = cacheFile
	cfg.CachePersistenceMaxAge = 1 * time.Hour

	// Create a test cache file with old timestamp
	snapshot := CacheSnapshot{
		Entries: []CacheEntry{
			{
				Key:        "example.com:1",
				Answers:    []string{"example.com.\t300\tIN\tA\t1.2.3.4"},
				Expiration: time.Now().Add(5 * time.Minute),
			},
		},
		Timestamp: time.Now().Add(-2 * time.Hour), // Old timestamp
	}

	data, _ := json.MarshalIndent(snapshot, "", "  ")
	err := os.WriteFile(cacheFile, data, 0644)
	require.NoError(t, err)

	// Load the cache
	err = LoadCacheFromFile()
	require.NoError(t, err)

	// Verify cache was not loaded due to age
	_, found := DnsCache.Get("example.com:1")
	assert.False(t, found)
}

func TestLoadCacheFromFile_SkipsExpiredEntries(t *testing.T) {
	_, cleanup := setupTestCache()
	defer cleanup()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "cache.json")

	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = cacheFile
	cfg.CachePersistenceMaxAge = 1 * time.Hour

	// Create a test cache file with expired entry
	snapshot := CacheSnapshot{
		Entries: []CacheEntry{
			{
				Key:        "expired.com:1",
				Answers:    []string{"expired.com.\t300\tIN\tA\t1.2.3.4"},
				Expiration: time.Now().Add(-1 * time.Minute), // Already expired
			},
			{
				Key:        "valid.com:1",
				Answers:    []string{"valid.com.\t300\tIN\tA\t5.6.7.8"},
				Expiration: time.Now().Add(5 * time.Minute),
			},
		},
		Timestamp: time.Now(),
	}

	data, _ := json.MarshalIndent(snapshot, "", "  ")
	err := os.WriteFile(cacheFile, data, 0644)
	require.NoError(t, err)

	// Load the cache
	err = LoadCacheFromFile()
	require.NoError(t, err)

	// Verify only valid entry was loaded
	_, found := DnsCache.Get("expired.com:1")
	assert.False(t, found)
	_, found = DnsCache.Get("valid.com:1")
	assert.True(t, found)
}

func TestLoadCacheFromFile_InvalidJSON(t *testing.T) {
	_, cleanup := setupTestCache()
	defer cleanup()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "cache.json")

	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = cacheFile

	// Write invalid JSON
	err := os.WriteFile(cacheFile, []byte("invalid json"), 0644)
	require.NoError(t, err)

	err = LoadCacheFromFile()
	assert.Error(t, err)
}

func TestLoadCacheFromFile_DisabledPersistence(t *testing.T) {
	_, cleanup := setupTestCache()
	defer cleanup()

	cfg = config.Get()
	cfg.EnableCachePersistence = false

	err := LoadCacheFromFile()
	assert.NoError(t, err)
}

func TestStartStopCachePersistence(t *testing.T) {
	_, cleanup := setupTestCache()
	defer cleanup()

	tempDir := t.TempDir()
	cacheFile := filepath.Join(tempDir, "cache.json")

	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = cacheFile
	cfg.CachePersistenceInterval = 100 * time.Millisecond

	// Add test data
	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	DnsCache.Set("example.com:1", []dns.RR{rr}, 5*time.Minute)

	StartCachePersistence()

	// Wait for at least one persistence cycle
	time.Sleep(200 * time.Millisecond)

	// Stop persistence
	StopCachePersistence()

	// Verify file was created
	_, err := os.Stat(cacheFile)
	assert.NoError(t, err)
}

func TestCacheSnapshot_Serialization(t *testing.T) {
	now := time.Now()
	snapshot := CacheSnapshot{
		Entries: []CacheEntry{
			{
				Key:        "test.com:1",
				Answers:    []string{"test.com.\t300\tIN\tA\t1.2.3.4"},
				Expiration: now.Add(5 * time.Minute),
				AccessInfo: &AccessInfo{
					AccessCount:      10,
					LastAccessNanos:  now.UnixNano(),
					FirstAccessNanos: now.Add(-1 * time.Hour).UnixNano(),
					Domain:           "test.com",
					QueryType:        1,
				},
			},
		},
		Timestamp: now,
	}

	// Serialize
	data, err := json.Marshal(snapshot)
	require.NoError(t, err)

	// Deserialize
	var deserialized CacheSnapshot
	err = json.Unmarshal(data, &deserialized)
	require.NoError(t, err)

	assert.Len(t, deserialized.Entries, 1)
	assert.Equal(t, "test.com:1", deserialized.Entries[0].Key)
	assert.NotNil(t, deserialized.Entries[0].AccessInfo)
	assert.Equal(t, int64(10), deserialized.Entries[0].AccessInfo.AccessCount)
}

func BenchmarkSaveCacheToFile(b *testing.B) {
	DnsCache = cache.New(5*time.Minute, 10*time.Minute)
	tempDir := b.TempDir()
	cacheFile := filepath.Join(tempDir, "cache.json")

	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = cacheFile

	// Populate cache with test data
	for i := 0; i < 100; i++ {
		rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
		DnsCache.Set("example"+string(rune(i))+".com:1", []dns.RR{rr}, 5*time.Minute)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SaveCacheToFile()
	}
}

func BenchmarkLoadCacheFromFile(b *testing.B) {
	DnsCache = cache.New(5*time.Minute, 10*time.Minute)
	tempDir := b.TempDir()
	cacheFile := filepath.Join(tempDir, "cache.json")

	cfg = config.Get()
	cfg.EnableCachePersistence = true
	cfg.CachePersistenceFile = cacheFile
	cfg.CachePersistenceMaxAge = 1 * time.Hour

	// Create test cache file
	entries := make([]CacheEntry, 100)
	for i := 0; i < 100; i++ {
		entries[i] = CacheEntry{
			Key:        "example" + string(rune(i)) + ".com:1",
			Answers:    []string{"example.com.\t300\tIN\tA\t1.2.3.4"},
			Expiration: time.Now().Add(5 * time.Minute),
		}
	}

	snapshot := CacheSnapshot{
		Entries:   entries,
		Timestamp: time.Now(),
	}

	data, _ := json.MarshalIndent(snapshot, "", "  ")
	_ = os.WriteFile(cacheFile, data, 0644)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = LoadCacheFromFile()
	}
}
