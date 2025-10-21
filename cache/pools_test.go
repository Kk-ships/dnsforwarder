package cache

import (
	"strings"
	"sync"
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyBuilderPool(t *testing.T) {
	// Get a builder from the pool
	builder := keyBuilderPool.Get().(*strings.Builder)
	require.NotNil(t, builder)

	// Use the builder
	builder.WriteString("test")
	assert.Equal(t, "test", builder.String())

	// Reset and return to pool
	builder.Reset()
	keyBuilderPool.Put(builder)

	// Get another builder - might be the same one
	builder2 := keyBuilderPool.Get().(*strings.Builder)
	require.NotNil(t, builder2)

	// Should be empty after reset
	assert.Equal(t, 0, builder2.Len())
}

func TestKeyBuilderPool_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	numGoroutines := 100

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			builder := keyBuilderPool.Get().(*strings.Builder)
			defer func() {
				builder.Reset()
				keyBuilderPool.Put(builder)
			}()

			builder.WriteString("test")
			assert.Contains(t, builder.String(), "test")
		}(i)
	}

	wg.Wait()
}

func TestDNSRRSlicePool(t *testing.T) {
	// Get a slice from the pool
	slice := getDNSRRSlice()
	assert.NotNil(t, slice)
	assert.Equal(t, 0, len(slice))
	assert.True(t, cap(slice) >= 16) // Should have pre-allocated capacity

	// Add some data
	rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
	slice = append(slice, rr)
	assert.Len(t, slice, 1)

	// Return to pool
	putDNSRRSlice(slice)

	// Get another slice
	slice2 := getDNSRRSlice()
	assert.Equal(t, 0, len(slice2)) // Should be reset
}

func TestDNSRRSlicePool_LargeSlice(t *testing.T) {
	// Create a slice larger than the pool limit
	slice := getDNSRRSlice()

	// Grow it beyond the pool limit
	for i := 0; i < 300; i++ {
		rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
		slice = append(slice, rr)
	}

	assert.Len(t, slice, 300)
	assert.True(t, cap(slice) > 256)

	// Return to pool - should not be added due to size
	putDNSRRSlice(slice)

	// Get a new slice - should be a different one
	slice2 := getDNSRRSlice()
	assert.True(t, cap(slice2) <= 256)
}

func TestDNSRRSlicePool_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	numGoroutines := 100

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()

			slice := getDNSRRSlice()
			rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
			slice = append(slice, rr)

			assert.Len(t, slice, 1)
			putDNSRRSlice(slice)
		}()
	}

	wg.Wait()
}

func TestStringSlicePool(t *testing.T) {
	// Get a slice from the pool
	slice := getStringSlice()
	assert.NotNil(t, slice)
	assert.Equal(t, 0, len(slice))
	assert.True(t, cap(slice) >= 16) // Should have pre-allocated capacity

	// Add some data
	slice = append(slice, "test1", "test2")
	assert.Len(t, slice, 2)

	// Return to pool
	putStringSlice(slice)

	// Get another slice
	slice2 := getStringSlice()
	assert.Equal(t, 0, len(slice2)) // Should be reset
}

func TestStringSlicePool_LargeSlice(t *testing.T) {
	// Create a slice larger than the pool limit
	slice := getStringSlice()

	// Grow it beyond the pool limit
	for i := 0; i < 300; i++ {
		slice = append(slice, "test")
	}

	assert.Len(t, slice, 300)
	assert.True(t, cap(slice) > 256)

	// Return to pool - should not be added due to size
	putStringSlice(slice)

	// Get a new slice - should be a different one
	slice2 := getStringSlice()
	assert.True(t, cap(slice2) <= 256)
}

func TestStringSlicePool_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	numGoroutines := 100

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			slice := getStringSlice()
			slice = append(slice, "test")

			assert.Len(t, slice, 1)
			putStringSlice(slice)
		}(i)
	}

	wg.Wait()
}

func TestPoolReuse(t *testing.T) {
	// Test that pools actually reuse objects

	// DNS RR slice pool
	slice1 := getDNSRRSlice()
	putDNSRRSlice(slice1)

	slice2 := getDNSRRSlice()

	// Can't guarantee they're the same due to concurrent access,
	// but the capacity should be maintained
	assert.True(t, cap(slice2) >= 16)

	// String slice pool
	strSlice1 := getStringSlice()
	putStringSlice(strSlice1)

	strSlice2 := getStringSlice()

	assert.True(t, cap(strSlice2) >= 16)
}

func BenchmarkKeyBuilderPool_Get(b *testing.B) {
	for i := 0; i < b.N; i++ {
		builder := keyBuilderPool.Get().(*strings.Builder)
		builder.Reset()
		keyBuilderPool.Put(builder)
	}
}

func BenchmarkKeyBuilderPool_WithoutPool(b *testing.B) {
	for i := 0; i < b.N; i++ {
		builder := &strings.Builder{}
		builder.Grow(128)
		_ = builder
	}
}

func BenchmarkDNSRRSlicePool_Get(b *testing.B) {
	for i := 0; i < b.N; i++ {
		slice := getDNSRRSlice()
		putDNSRRSlice(slice)
	}
}

func BenchmarkDNSRRSlicePool_WithoutPool(b *testing.B) {
	for i := 0; i < b.N; i++ {
		slice := make([]dns.RR, 0, 16)
		_ = slice
	}
}

func BenchmarkStringSlicePool_Get(b *testing.B) {
	for i := 0; i < b.N; i++ {
		slice := getStringSlice()
		putStringSlice(slice)
	}
}

func BenchmarkStringSlicePool_WithoutPool(b *testing.B) {
	for i := 0; i < b.N; i++ {
		slice := make([]string, 0, 16)
		_ = slice
	}
}

func BenchmarkKeyBuilderPool_Concurrent(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			builder := keyBuilderPool.Get().(*strings.Builder)
			builder.WriteString("test.com")
			builder.Reset()
			keyBuilderPool.Put(builder)
		}
	})
}

func BenchmarkDNSRRSlicePool_Concurrent(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			slice := getDNSRRSlice()
			rr, _ := dns.NewRR("example.com. 300 IN A 1.2.3.4")
			slice = append(slice, rr)
			putDNSRRSlice(slice)
		}
	})
}

func BenchmarkStringSlicePool_Concurrent(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			slice := getStringSlice()
			slice = append(slice, "test")
			putStringSlice(slice)
		}
	})
}
