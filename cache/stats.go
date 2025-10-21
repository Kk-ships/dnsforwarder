package cache

import (
	"time"

	"dnsloadbalancer/logutil"
	"dnsloadbalancer/metric"
)

// StartCacheStatsLogger starts periodic logging of cache statistics
func StartCacheStatsLogger() {
	ticker := time.NewTicker(cfg.DNSStatslog)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			if DnsCache != nil {
				items := DnsCache.Items()
				itemCount := len(items)
				if itemCount == 0 {
					logutil.Logger.Warn("No cache entries found")
				} else {
					logutil.Logger.Infof("Cache Entries: %d", itemCount)
					if EnableMetrics {
						metric.GetFastMetricsInstance().FastUpdateCacheSize(itemCount)
					}
				}
			}
		}
	}()
}
