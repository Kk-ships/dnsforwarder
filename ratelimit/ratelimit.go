// Package ratelimit provides comprehensive rate limiting functionality for the DNS forwarder
// Features include:
// - Per-client rate limiting with sliding windows
// - Adaptive throttling based on query patterns
// - Suspicious behavior detection
// - IP-based blocking with automatic expiration
// - Integration with metrics system
package ratelimit

import (
	"sync"
	"sync/atomic"
	"time"

	"dnsloadbalancer/logutil"
	"dnsloadbalancer/metric"
)

// RateLimitResult represents the outcome of a rate limit check
type RateLimitResult struct {
	Allowed       bool          // Whether the request is allowed
	RemainingRate int           // Remaining requests in the current window
	ResetTime     time.Time     // When the rate limit resets
	RetryAfter    time.Duration // How long to wait before retrying
	Reason        string        // Reason for denial (if denied)
}

// ClientBucket represents rate limiting data for a single client
type ClientBucket struct {
	// Sliding window counters
	requests      []int64   // Request counts per time slot
	lastUpdate    time.Time // Last time the bucket was updated
	totalRequests int64     // Total requests in current window

	// Behavioral analysis
	burstCount     int32     // Number of burst periods detected
	lastBurstTime  time.Time // Last time a burst was detected
	suspicionLevel int32     // Current suspicion level (0-100)

	// Blocking state
	blockedUntil time.Time // When the client will be unblocked
	blockReason  string    // Reason for blocking

	mutex sync.RWMutex // Protects bucket data
}

// RateLimitConfig holds configuration for rate limiting
type RateLimitConfig struct {
	// Basic rate limiting
	MaxRequestsPerSecond int           // Maximum requests per second per client
	MaxRequestsPerMinute int           // Maximum requests per minute per client
	MaxRequestsPerHour   int           // Maximum requests per hour per client
	WindowSize           time.Duration // Size of sliding window
	WindowSlots          int           // Number of slots in sliding window

	// Burst detection
	BurstThreshold     int           // Requests per second to trigger burst detection
	BurstWindow        time.Duration // Time window for burst detection
	MaxBurstsPerMinute int           // Maximum allowed bursts per minute

	// Adaptive throttling
	AdaptiveEnabled    bool    // Enable adaptive throttling
	SuspicionThreshold int     // Suspicion level to trigger throttling
	ThrottleMultiplier float64 // Rate reduction multiplier when throttling

	// Blocking
	BlockingEnabled bool          // Enable automatic blocking
	BlockDuration   time.Duration // How long to block suspicious clients
	BlockThreshold  int           // Suspicion level to trigger blocking

	// Cleanup
	CleanupInterval time.Duration // How often to cleanup old entries
	ClientTimeout   time.Duration // How long to keep inactive client data
}

// RateLimiter manages rate limiting for DNS clients
type RateLimiter struct {
	config  *RateLimitConfig
	clients sync.Map // map[string]*ClientBucket
	metrics *metric.FastMetricsRecorder

	// Global statistics
	totalRequests   uint64
	blockedRequests uint64
	allowedRequests uint64

	// Cleanup
	stopCleanup chan struct{}
	cleanupDone sync.WaitGroup
}

// DefaultRateLimitConfig returns a sensible default configuration
func DefaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		// Allow 100 QPS, 3000 QPM, 50K QPH per client by default
		MaxRequestsPerSecond: 100,
		MaxRequestsPerMinute: 3000,
		MaxRequestsPerHour:   50000,
		WindowSize:           time.Minute,
		WindowSlots:          60, // 1-second slots

		// Burst detection: >200 QPS triggers burst, max 5 bursts/minute
		BurstThreshold:     200,
		BurstWindow:        time.Second * 5,
		MaxBurstsPerMinute: 5,

		// Adaptive throttling
		AdaptiveEnabled:    true,
		SuspicionThreshold: 30,
		ThrottleMultiplier: 0.5, // Reduce rate by 50% when throttling

		// Blocking: Block for 5 minutes when suspicion >= 80
		BlockingEnabled: true,
		BlockDuration:   time.Minute * 5,
		BlockThreshold:  80,

		// Cleanup every 5 minutes, timeout clients after 30 minutes
		CleanupInterval: time.Minute * 5,
		ClientTimeout:   time.Minute * 30,
	}
}

// NewRateLimiter creates a new rate limiter with the given configuration
func NewRateLimiter(config *RateLimitConfig, metrics *metric.FastMetricsRecorder) *RateLimiter {
	if config == nil {
		config = DefaultRateLimitConfig()
	}

	rl := &RateLimiter{
		config:      config,
		metrics:     metrics,
		stopCleanup: make(chan struct{}),
	}

	// Start cleanup goroutine
	rl.cleanupDone.Add(1)
	go rl.cleanupLoop()

	logutil.Logger.Infof("Rate limiter initialized: %d QPS, %d QPM, %d QPH per client",
		config.MaxRequestsPerSecond, config.MaxRequestsPerMinute, config.MaxRequestsPerHour)

	return rl
}

// CheckRateLimit checks if a client's request should be allowed
func (rl *RateLimiter) CheckRateLimit(clientIP string) *RateLimitResult {
	atomic.AddUint64(&rl.totalRequests, 1)

	// Get or create client bucket
	bucketInterface, _ := rl.clients.LoadOrStore(clientIP, &ClientBucket{
		requests:   make([]int64, rl.config.WindowSlots),
		lastUpdate: time.Now(),
	})
	bucket := bucketInterface.(*ClientBucket)

	bucket.mutex.Lock()
	defer bucket.mutex.Unlock()

	now := time.Now()

	// Check if client is currently blocked
	if rl.config.BlockingEnabled && now.Before(bucket.blockedUntil) {
		atomic.AddUint64(&rl.blockedRequests, 1)
		if rl.metrics != nil {
			rl.metrics.FastRecordError("rate_limit_blocked", "client_blocked")
			rl.metrics.FastRecordRateLimitBlocked("[masked]", bucket.blockReason)
		}

		return &RateLimitResult{
			Allowed:    false,
			RetryAfter: time.Until(bucket.blockedUntil),
			ResetTime:  bucket.blockedUntil,
			Reason:     bucket.blockReason,
		}
	}

	// Update sliding window
	rl.updateSlidingWindow(bucket, now)

	// Apply adaptive throttling if enabled
	limit := int64(rl.config.MaxRequestsPerSecond)
	if rl.config.AdaptiveEnabled && bucket.suspicionLevel >= int32(rl.config.SuspicionThreshold) {
		limit = int64(float64(limit) * rl.config.ThrottleMultiplier)
	}

	// Check if we need to block this client first (before allowing the request)
	if rl.config.BlockingEnabled && bucket.suspicionLevel >= int32(rl.config.BlockThreshold) {
		rl.blockClient(bucket, now, "suspicious_behavior")
		atomic.AddUint64(&rl.blockedRequests, 1)
		if rl.metrics != nil {
			rl.metrics.FastRecordError("rate_limit_blocked", "client_blocked")
			rl.metrics.FastRecordRateLimitBlocked("[masked]", "suspicious_behavior")
		}

		return &RateLimitResult{
			Allowed:    false,
			RetryAfter: rl.config.BlockDuration,
			ResetTime:  now.Add(rl.config.BlockDuration),
			Reason:     "suspicious_behavior",
		}
	}

	// Check rate limits - check current second's requests against per-second limit
	currentSlot := rl.getSlotIndex(now)
	currentSecondRequests := bucket.requests[currentSlot]

	if currentSecondRequests >= limit {
		atomic.AddUint64(&rl.blockedRequests, 1)
		rl.increaseSuspicion(bucket, "rate_limit_exceeded")

		if rl.metrics != nil {
			rl.metrics.FastRecordError("rate_limit_exceeded", "qps_limit")
			rl.metrics.FastRecordRateLimitBlocked("[masked]", "QPS limit exceeded")
		}

		return &RateLimitResult{
			Allowed:       false,
			RemainingRate: int(limit - currentSecondRequests),
			ResetTime:     now.Truncate(time.Second).Add(time.Second),
			RetryAfter:    time.Second - time.Duration(now.Nanosecond()),
			Reason:        "QPS limit exceeded",
		}
	}

	// Increment request count
	slotIndex := rl.getSlotIndex(now)
	bucket.requests[slotIndex]++
	bucket.totalRequests++

	atomic.AddUint64(&rl.allowedRequests, 1)

	// Record allowed request metric
	if rl.metrics != nil {
		rl.metrics.FastRecordRateLimitAllowed("[masked]")
	}

	// Check for burst detection after incrementing
	rl.detectBurst(bucket, now)

	// Check for suspicious patterns
	rl.analyzeBehavior(bucket, now)

	return &RateLimitResult{
		Allowed:       true,
		RemainingRate: int(limit - bucket.requests[slotIndex]),
		ResetTime:     now.Truncate(time.Second).Add(time.Second),
		RetryAfter:    0,
		Reason:        "",
	}
}

// updateSlidingWindow updates the sliding window counters
func (rl *RateLimiter) updateSlidingWindow(bucket *ClientBucket, now time.Time) {
	timeDiff := now.Sub(bucket.lastUpdate)
	if timeDiff < time.Second {
		return // No need to update if less than a second has passed
	}

	slotsToAdvance := int(timeDiff.Seconds())
	if slotsToAdvance >= rl.config.WindowSlots {
		// Clear all slots if we've advanced more than the window size
		for i := range bucket.requests {
			bucket.requests[i] = 0
		}
		bucket.totalRequests = 0
	} else {
		// Clear the slots that should no longer be counted
		lastUpdateSlot := rl.getSlotIndex(bucket.lastUpdate)

		// Clear slots between the last update and current time
		for i := 1; i <= slotsToAdvance; i++ {
			slotToClear := (lastUpdateSlot + i) % rl.config.WindowSlots
			bucket.totalRequests -= bucket.requests[slotToClear]
			bucket.requests[slotToClear] = 0
		}
	}

	bucket.lastUpdate = now
}

// getSlotIndex returns the slot index for a given time
func (rl *RateLimiter) getSlotIndex(t time.Time) int {
	return int(t.Unix()) % rl.config.WindowSlots
}

// detectBurst detects burst patterns in client requests
func (rl *RateLimiter) detectBurst(bucket *ClientBucket, now time.Time) {
	// Check if the current second exceeds the burst threshold
	currentSlot := rl.getSlotIndex(now)
	currentSecondRequests := bucket.requests[currentSlot]

	if currentSecondRequests >= int64(rl.config.BurstThreshold) {
		if now.Sub(bucket.lastBurstTime) > rl.config.BurstWindow {
			bucket.burstCount++
			bucket.lastBurstTime = now

			rl.increaseSuspicion(bucket, "burst_detected")

			if rl.metrics != nil {
				rl.metrics.FastRecordError("burst_detected", "client_behavior")
			}

			logutil.Logger.Warnf("Burst detected from client %s: %d requests in current second",
				"[masked]", currentSecondRequests)
		}
	}
}

// analyzeBehavior analyzes client behavior for suspicious patterns
func (rl *RateLimiter) analyzeBehavior(bucket *ClientBucket, now time.Time) {
	// Check for sustained high request rates
	if bucket.totalRequests > int64(rl.config.MaxRequestsPerSecond*3/4) {
		rl.increaseSuspicion(bucket, "high_sustained_rate")
	}

	// Check for too many bursts in a minute
	if bucket.burstCount > int32(rl.config.MaxBurstsPerMinute) &&
		now.Sub(bucket.lastBurstTime) < time.Minute {
		rl.increaseSuspicion(bucket, "excessive_bursts")
	}

	// Gradually decrease suspicion over time if no issues
	if now.Sub(bucket.lastBurstTime) > time.Minute*5 && bucket.suspicionLevel > 0 {
		atomic.AddInt32(&bucket.suspicionLevel, -1)
	}
}

// increaseSuspicion increases the suspicion level for a client
func (rl *RateLimiter) increaseSuspicion(bucket *ClientBucket, reason string) {
	oldLevel := atomic.LoadInt32(&bucket.suspicionLevel)
	newLevel := atomic.AddInt32(&bucket.suspicionLevel, 10)

	if newLevel > 100 {
		atomic.StoreInt32(&bucket.suspicionLevel, 100)
		newLevel = 100
	}

	if rl.metrics != nil {
		rl.metrics.FastRecordError("suspicion_increased", reason)
		// Record suspicious client metric if suspicion level is significant
		if newLevel >= int32(rl.config.SuspicionThreshold) { // Report clients suspicious
			rl.metrics.FastRecordRateLimitSuspiciousClient("[masked]", newLevel)
		}
	}

	logutil.Logger.Debugf("Suspicion level increased for client: %d -> %d (reason: %s)",
		oldLevel, newLevel, reason)
}

// blockClient blocks a client for the configured duration
func (rl *RateLimiter) blockClient(bucket *ClientBucket, now time.Time, reason string) {
	bucket.blockedUntil = now.Add(rl.config.BlockDuration)
	bucket.blockReason = reason

	if rl.metrics != nil {
		rl.metrics.FastRecordError("client_blocked", reason)
		// Record the blocked client metric
		rl.metrics.FastRecordRateLimitBlocked("[masked]", reason)
	}

	logutil.Logger.Warnf("Client blocked for %v due to: %s",
		rl.config.BlockDuration, reason)
}

// IsBlocked checks if a client is currently blocked
func (rl *RateLimiter) IsBlocked(clientIP string) bool {
	bucketInterface, exists := rl.clients.Load(clientIP)
	if !exists {
		return false
	}

	bucket := bucketInterface.(*ClientBucket)
	bucket.mutex.RLock()
	defer bucket.mutex.RUnlock()

	return time.Now().Before(bucket.blockedUntil)
}

// UnblockClient manually unblocks a client
func (rl *RateLimiter) UnblockClient(clientIP string) {
	bucketInterface, exists := rl.clients.Load(clientIP)
	if !exists {
		return
	}

	bucket := bucketInterface.(*ClientBucket)
	bucket.mutex.Lock()
	defer bucket.mutex.Unlock()

	bucket.blockedUntil = time.Time{}
	bucket.blockReason = ""
	atomic.StoreInt32(&bucket.suspicionLevel, 0)

	logutil.Logger.Infof("Client %s manually unblocked", clientIP)
}

// GetClientStats returns statistics for a specific client
func (rl *RateLimiter) GetClientStats(clientIP string) map[string]interface{} {
	bucketInterface, exists := rl.clients.Load(clientIP)
	if !exists {
		return map[string]interface{}{
			"exists": false,
		}
	}

	bucket := bucketInterface.(*ClientBucket)
	bucket.mutex.RLock()
	defer bucket.mutex.RUnlock()

	return map[string]interface{}{
		"exists":          true,
		"total_requests":  bucket.totalRequests,
		"burst_count":     bucket.burstCount,
		"suspicion_level": bucket.suspicionLevel,
		"is_blocked":      time.Now().Before(bucket.blockedUntil),
		"blocked_until":   bucket.blockedUntil,
		"block_reason":    bucket.blockReason,
		"last_burst_time": bucket.lastBurstTime,
		"last_update":     bucket.lastUpdate,
	}
}

// GetGlobalStats returns global rate limiting statistics
func (rl *RateLimiter) GetGlobalStats() map[string]interface{} {
	clientCount := 0
	blockedCount := 0
	suspiciousCount := 0

	now := time.Now()
	rl.clients.Range(func(key, value interface{}) bool {
		clientCount++
		bucket := value.(*ClientBucket)
		bucket.mutex.RLock()

		if now.Before(bucket.blockedUntil) {
			blockedCount++
		}
		if bucket.suspicionLevel >= int32(rl.config.SuspicionThreshold) {
			suspiciousCount++
		}

		bucket.mutex.RUnlock()
		return true
	})

	return map[string]interface{}{
		"total_requests":     atomic.LoadUint64(&rl.totalRequests),
		"blocked_requests":   atomic.LoadUint64(&rl.blockedRequests),
		"allowed_requests":   atomic.LoadUint64(&rl.allowedRequests),
		"active_clients":     clientCount,
		"blocked_clients":    blockedCount,
		"suspicious_clients": suspiciousCount,
		"config": map[string]interface{}{
			"max_qps":         rl.config.MaxRequestsPerSecond,
			"max_qpm":         rl.config.MaxRequestsPerMinute,
			"max_qph":         rl.config.MaxRequestsPerHour,
			"burst_threshold": rl.config.BurstThreshold,
			"block_duration":  rl.config.BlockDuration.String(),
		},
	}
}

// cleanupLoop periodically cleans up old client entries
func (rl *RateLimiter) cleanupLoop() {
	defer rl.cleanupDone.Done()

	ticker := time.NewTicker(rl.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.cleanup()
		case <-rl.stopCleanup:
			return
		}
	}
}

// cleanup removes old and inactive client entries
func (rl *RateLimiter) cleanup() {
	now := time.Now()
	toDelete := make([]string, 0)

	rl.clients.Range(func(key, value interface{}) bool {
		clientIP := key.(string)
		bucket := value.(*ClientBucket)

		bucket.mutex.RLock()
		shouldDelete := now.Sub(bucket.lastUpdate) > rl.config.ClientTimeout &&
			now.After(bucket.blockedUntil) &&
			bucket.totalRequests == 0
		bucket.mutex.RUnlock()

		if shouldDelete {
			toDelete = append(toDelete, clientIP)
		}

		return true
	})

	for _, clientIP := range toDelete {
		rl.clients.Delete(clientIP)
	}

	if len(toDelete) > 0 {
		logutil.Logger.Debugf("Rate limiter cleanup: removed %d inactive clients", len(toDelete))
	}
}

// Shutdown gracefully stops the rate limiter
func (rl *RateLimiter) Shutdown() {
	close(rl.stopCleanup)
	rl.cleanupDone.Wait()
	logutil.Logger.Info("Rate limiter shut down")
}

// Global rate limiter instance using atomic.Value for lock-free reads
var (
	globalRateLimiterValue atomic.Value // stores *RateLimiter
	rateLimiterOnce        sync.Once
)

// InitializeGlobalRateLimiter initializes the global rate limiter
func InitializeGlobalRateLimiter(enableRateLimit bool) {
	if !enableRateLimit {
		return
	}

	rateLimiterOnce.Do(func() {
		rateLimitConfig := LoadRateLimitConfigFromEnv()
		metrics := metric.GetFastMetricsInstance()

		rateLimiter := NewRateLimiter(rateLimitConfig, metrics)
		globalRateLimiterValue.Store(rateLimiter)

		logutil.Logger.Info("Global rate limiter initialized")
	})
}

// GetGlobalRateLimiter returns the global rate limiter instance
// Optimized for high-frequency read access using atomic.Value
func GetGlobalRateLimiter() *RateLimiter {
	// Use atomic.Value.Load() for lock-free read access
	if rl := globalRateLimiterValue.Load(); rl != nil {
		return rl.(*RateLimiter)
	}
	return nil
}

// ShutdownGlobalRateLimiter shuts down the global rate limiter
func ShutdownGlobalRateLimiter() {
	if rl := globalRateLimiterValue.Load(); rl != nil {
		rateLimiter := rl.(*RateLimiter)
		rateLimiter.Shutdown()
		globalRateLimiterValue.Store((*RateLimiter)(nil))
	}
}
