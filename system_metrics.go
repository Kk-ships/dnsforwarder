package main

import (
	"runtime"
)

// getGoroutineCount returns the current number of goroutines
func getGoroutineCount() int {
	return runtime.NumGoroutine()
}

// getMemoryUsage returns the current memory usage in bytes
func getMemoryUsage() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Alloc
}

