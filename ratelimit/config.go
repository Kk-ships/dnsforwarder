package ratelimit

import (
	"dnsloadbalancer/config"
)

var cfg = config.Get()

// LoadRateLimitConfigFromEnv loads rate limiting configuration from environment variables
func LoadRateLimitConfigFromEnv() *RateLimitConfig {
	return &RateLimitConfig{
		// Basic rate limiting - default to generous limits
		MaxRequestsPerSecond: cfg.MaxRequestsPerSecond,
		MaxRequestsPerMinute: cfg.MaxRequestsPerMinute,
		MaxRequestsPerHour:   cfg.MaxRequestsPerHour,
		WindowSize:           cfg.WindowSize,
		WindowSlots:          cfg.WindowSlots,

		// Burst detection
		BurstThreshold:     cfg.BurstThreshold,
		BurstWindow:        cfg.BurstWindow,
		MaxBurstsPerMinute: cfg.MaxBurstsPerMinute,

		// Adaptive throttling
		AdaptiveEnabled:    cfg.AdaptiveEnabled,
		SuspicionThreshold: cfg.SuspicionThreshold,
		ThrottleMultiplier: cfg.ThrottleMultiplier,

		// Blocking
		BlockingEnabled: cfg.BlockingEnabled,
		BlockDuration:   cfg.BlockDuration,
		BlockThreshold:  cfg.BlockThreshold,

		// Cleanup
		CleanupInterval: cfg.CleanupInterval,
		ClientTimeout:   cfg.ClientTimeout,
	}
}
