// Package querycoalescing provides DNS query coalescing functionality to combine
// identical in-flight requests, reducing upstream DNS server load and improving
// response times for duplicate queries.
package querycoalescing

import (
	"context"
	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/metric"
	"fmt"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// QueryResult represents the result of a DNS query
type QueryResult struct {
	Answers []dns.RR
	Error   error
}

// InFlightQuery represents an ongoing DNS query with waiting clients
type InFlightQuery struct {
	// Channels for each waiting client
	waiters []chan QueryResult
	// Mutex to protect waiters slice
	waitersMu sync.Mutex
	// Number of clients waiting for this query
	waiterCount int
	// Timestamp when the query was initiated
	startTime time.Time
	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
	// Flag to indicate if query is completed
	completed bool
	// Result of the query (cached after completion)
	result QueryResult
}

// QueryCoalescer manages in-flight DNS queries to prevent duplicate upstream requests
type QueryCoalescer struct {
	// Map of query keys to in-flight queries
	inFlight sync.Map // map[string]*InFlightQuery
	// Metrics instance for recording coalescing stats
	metrics *metric.FastMetricsRecorder
	// Configuration
	cfg *config.Config
	// Channel pool for result channels to reduce allocations
	channelPool sync.Pool
}

// QueryCoalescerInterface defines the interface for query coalescing
type QueryCoalescerInterface interface {
	CoalesceQuery(key string, queryFunc func() ([]dns.RR, error)) ([]dns.RR, error)
	GetStats() CoalescingStats
	Cleanup()
}

// CoalescingStats provides statistics about query coalescing performance
type CoalescingStats struct {
	TotalQueries     int64   // Total number of queries processed
	CoalescedQueries int64   // Number of queries that were coalesced (didn't hit upstream)
	InFlightCount    int     // Current number of in-flight queries
	AvgWaiters       float64 // Average number of waiters per query
	MaxWaiters       int     // Maximum waiters seen for a single query
}

// Global instance
var globalCoalescer *QueryCoalescer
var coalescerOnce sync.Once

// GetGlobalCoalescer returns the singleton query coalescer instance
func GetGlobalCoalescer() *QueryCoalescer {
	coalescerOnce.Do(func() {
		globalCoalescer = NewQueryCoalescer(config.Get(), metric.GetFastMetricsInstance())
	})
	return globalCoalescer
}

// NewQueryCoalescer creates a new query coalescer
func NewQueryCoalescer(cfg *config.Config, metrics *metric.FastMetricsRecorder) *QueryCoalescer {
	qc := &QueryCoalescer{
		cfg:     cfg,
		metrics: metrics,
		channelPool: sync.Pool{
			New: func() interface{} {
				// Create a buffered channel to avoid blocking
				return make(chan QueryResult, 1)
			},
		},
	}

	// Start cleanup routine if coalescing is enabled
	if cfg.EnableQueryCoalescing {
		go qc.cleanupRoutine()
	}

	return qc
}

// CoalesceQuery performs query coalescing for the given key and query function
func (qc *QueryCoalescer) CoalesceQuery(key string, queryFunc func() ([]dns.RR, error)) ([]dns.RR, error) {
	// If coalescing is disabled, execute query directly
	if !qc.cfg.EnableQueryCoalescing {
		return queryFunc()
	}

	// Try to get existing in-flight query
	if value, exists := qc.inFlight.Load(key); exists {
		inFlight := value.(*InFlightQuery)

		// Check if query is already completed
		inFlight.waitersMu.Lock()
		if inFlight.completed {
			result := inFlight.result
			inFlight.waitersMu.Unlock()
			return result.Answers, result.Error
		}

		// Add ourselves as a waiter
		waiterChan := qc.getChannel()
		inFlight.waiters = append(inFlight.waiters, waiterChan)
		inFlight.waiterCount++
		waiterCount := inFlight.waiterCount
		inFlight.waitersMu.Unlock()

		if qc.cfg.EnableMetrics {
			qc.metrics.FastRecordQueryCoalesced()
		}

		logutil.Logger.Debugf("Query coalesced for key: %s (waiters: %d)", key, waiterCount)

		// Wait for result
		select {
		case result := <-waiterChan:
			qc.returnChannel(waiterChan)
			return result.Answers, result.Error
		case <-inFlight.ctx.Done():
			qc.returnChannel(waiterChan)
			logutil.Logger.Warnf("Query coalescing timeout for key: %s", key)
			return queryFunc()
		}
	}

	// Create new in-flight query
	ctx, cancel := context.WithTimeout(context.Background(), qc.cfg.QueryCoalescingTimeout)
	newInFlight := &InFlightQuery{
		waiters:     make([]chan QueryResult, 0, 1),
		waiterCount: 1,
		startTime:   time.Now(),
		ctx:         ctx,
		cancel:      cancel,
		completed:   false,
	}

	// Try to store it
	if _, loaded := qc.inFlight.LoadOrStore(key, newInFlight); loaded {
		// Race condition: another goroutine created the query first
		newInFlight.cancel()

		// Retry with the existing query
		return qc.CoalesceQuery(key, queryFunc)
	}

	// We are the first request, execute the query
	go func() {
		defer func() {
			qc.inFlight.Delete(key)
			newInFlight.cancel()
		}()

		// Execute the query
		answers, err := queryFunc()

		result := QueryResult{
			Answers: answers,
			Error:   err,
		}

		// Mark as completed and broadcast to all waiters
		newInFlight.waitersMu.Lock()
		newInFlight.completed = true
		newInFlight.result = result
		waiters := make([]chan QueryResult, len(newInFlight.waiters))
		copy(waiters, newInFlight.waiters)
		waiterCount := newInFlight.waiterCount
		newInFlight.waitersMu.Unlock()

		// Send result to all waiters
		for _, waiterChan := range waiters {
			select {
			case waiterChan <- result:
			default:
				// Waiter may have timed out, skip
			}
		}

		logutil.Logger.Debugf("Query result broadcast for key: %s (waiters: %d)", key, waiterCount)
	}()

	// Add ourselves as a waiter and wait for result
	waiterChan := qc.getChannel()

	newInFlight.waitersMu.Lock()
	if newInFlight.completed {
		// Query completed while we were setting up
		result := newInFlight.result
		newInFlight.waitersMu.Unlock()
		qc.returnChannel(waiterChan)
		return result.Answers, result.Error
	}
	newInFlight.waiters = append(newInFlight.waiters, waiterChan)
	newInFlight.waitersMu.Unlock()

	// Wait for result
	select {
	case result := <-waiterChan:
		qc.returnChannel(waiterChan)
		return result.Answers, result.Error
	case <-newInFlight.ctx.Done():
		qc.returnChannel(waiterChan)
		logutil.Logger.Warnf("Query execution timeout for key: %s", key)
		return queryFunc()
	}
}

// getChannel gets a channel from the pool
func (qc *QueryCoalescer) getChannel() chan QueryResult {
	return qc.channelPool.Get().(chan QueryResult)
}

// returnChannel returns a channel to the pool after cleaning it
func (qc *QueryCoalescer) returnChannel(ch chan QueryResult) {
	// Drain the channel if there are any remaining items
	select {
	case <-ch:
	default:
	}
	qc.channelPool.Put(ch)
}

// GetStats returns current coalescing statistics
func (qc *QueryCoalescer) GetStats() CoalescingStats {
	stats := CoalescingStats{}

	// Count current in-flight queries and calculate waiter statistics
	var totalWaiters int
	var maxWaiters int
	inFlightCount := 0

	qc.inFlight.Range(func(key, value interface{}) bool {
		inFlightCount++
		inFlight := value.(*InFlightQuery)
		inFlight.waitersMu.Lock()
		waiters := inFlight.waiterCount
		inFlight.waitersMu.Unlock()

		totalWaiters += waiters
		if waiters > maxWaiters {
			maxWaiters = waiters
		}
		return true
	})

	stats.InFlightCount = inFlightCount
	stats.MaxWaiters = maxWaiters

	if inFlightCount > 0 {
		stats.AvgWaiters = float64(totalWaiters) / float64(inFlightCount)
	}

	// Get metrics from the metrics instance if available
	if qc.metrics != nil {
		// These would need to be implemented in the metrics interface
		// For now, we'll leave them as zero
		stats.TotalQueries = 0
		stats.CoalescedQueries = 0
	}

	return stats
}

// cleanupRoutine periodically cleans up stale in-flight queries
func (qc *QueryCoalescer) cleanupRoutine() {
	ticker := time.NewTicker(qc.cfg.QueryCoalescingCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		qc.cleanupStaleQueries()
	}
}

// cleanupStaleQueries removes queries that have been in-flight for too long
func (qc *QueryCoalescer) cleanupStaleQueries() {
	now := time.Now()
	staleCutoff := now.Add(-qc.cfg.QueryCoalescingTimeout)

	var staleKeys []string

	qc.inFlight.Range(func(key, value interface{}) bool {
		inFlight := value.(*InFlightQuery)
		if inFlight.startTime.Before(staleCutoff) {
			staleKeys = append(staleKeys, key.(string))
		}
		return true
	})

	// Clean up stale queries
	for _, key := range staleKeys {
		if value, ok := qc.inFlight.LoadAndDelete(key); ok {
			inFlight := value.(*InFlightQuery)
			inFlight.cancel()
			// Clean up waiter channels
			inFlight.waitersMu.Lock()
			for _, waiterChan := range inFlight.waiters {
				qc.returnChannel(waiterChan)
			}
			inFlight.waitersMu.Unlock()
			logutil.Logger.Debugf("Cleaned up stale query: %s", key)
		}
	}

	if len(staleKeys) > 0 {
		logutil.Logger.Debugf("Cleaned up %d stale queries", len(staleKeys))
	}
}

// Cleanup performs cleanup of the query coalescer
func (qc *QueryCoalescer) Cleanup() {
	// Cancel all in-flight queries
	qc.inFlight.Range(func(key, value interface{}) bool {
		inFlight := value.(*InFlightQuery)
		inFlight.cancel()
		// Clean up waiter channels
		inFlight.waitersMu.Lock()
		for _, waiterChan := range inFlight.waiters {
			qc.returnChannel(waiterChan)
		}
		inFlight.waitersMu.Unlock()
		return true
	})

	// Clear the map
	qc.inFlight = sync.Map{}

	logutil.Logger.Info("Query coalescer cleaned up")
}

// QueryKey generates a consistent key for query coalescing based on domain and query type
func QueryKey(domain string, qtype uint16, clientIP string, enableClientRouting bool) string {
	if enableClientRouting {
		// Include client IP in the key for client-specific routing
		return fmt.Sprintf("%s:%d:%s", domain, qtype, clientIP)
	}
	// Standard key without client IP for better coalescing
	return fmt.Sprintf("%s:%d", domain, qtype)
}
