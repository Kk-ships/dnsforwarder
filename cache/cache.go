package cache

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"dnsloadbalancer/config"
	"dnsloadbalancer/dnsresolver"
	"dnsloadbalancer/domainrouting"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/metric"

	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
)

type cacheEntry struct {
	Answers []dns.RR
}

var (
	cacheHits           int64
	cacheRequests       int64
	DnsCache            *cache.Cache
	DefaultDNSCacheTTL  time.Duration = 30 * time.Minute
	EnableMetrics       bool
	EnableClientRouting bool
	EnableDomainRouting bool
)

func Init(defaultDNSCacheTTL time.Duration, enableMetrics bool, _ interface{}, enableClientRouting bool, enableDomainRouting bool) {
	logutil.Logger.Debugf("Init: start, defaultDNSCacheTTL=%v, enableMetrics=%v, enableClientRouting=%v, enableDomainRouting=%v", defaultDNSCacheTTL, enableMetrics, enableClientRouting, enableDomainRouting)
	DnsCache = cache.New(defaultDNSCacheTTL, 2*defaultDNSCacheTTL)
	DefaultDNSCacheTTL = defaultDNSCacheTTL
	EnableMetrics = enableMetrics
	EnableClientRouting = enableClientRouting
	EnableDomainRouting = enableDomainRouting
	logutil.Logger.Debug("Init: end")
}

func CacheKey(domain string, qtype uint16) string {
	logutil.Logger.Debugf("CacheKey: start, domain=%s, qtype=%d", domain, qtype)
	var b strings.Builder
	b.Grow(len(domain) + 1 + 5)
	b.WriteString(domain)
	b.WriteByte(':')
	b.WriteString(strconv.FormatUint(uint64(qtype), 10))
	key := b.String()
	logutil.Logger.Debugf("CacheKey: end, key=%s", key)
	return key
}

func SaveToCache(key string, answers []dns.RR, ttl time.Duration) {
	logutil.Logger.Debugf("SaveToCache: start, key=%s, ttl=%v, answers_len=%d", key, ttl, len(answers))
	DnsCache.Set(key, answers, ttl)
	logutil.Logger.Debug("SaveToCache: end")
}

func LoadFromCache(key string) ([]dns.RR, bool) {
	logutil.Logger.Debugf("LoadFromCache: start, key=%s", key)
	val, found := DnsCache.Get(key)
	if !found {
		logutil.Logger.Debugf("LoadFromCache: key=%s not found", key)
		return nil, false
	}
	answers, ok := val.([]dns.RR)
	if !ok {
		logutil.Logger.Debugf("LoadFromCache: key=%s found but type assertion failed", key)
		return nil, false
	}
	logutil.Logger.Debugf("LoadFromCache: key=%s found, answers_len=%d", key, len(answers))
	return answers, true
}

func ResolverWithCache(domain string, qtype uint16, clientIP string) []dns.RR {
	logutil.Logger.Debugf("ResolverWithCache: start, domain=%s, qtype=%d, clientIP=%s", domain, qtype, clientIP)
	start := time.Now()
	key := CacheKey(domain, qtype)
	atomic.AddInt64(&cacheRequests, 1)

	qTypeStr := dns.TypeToString[qtype]

	if answers, ok := LoadFromCache(key); ok {
		atomic.AddInt64(&cacheHits, 1)
		if EnableMetrics {
			metric.MetricsRecorderInstance.RecordCacheHit()
			metric.MetricsRecorderInstance.RecordDNSQuery(qTypeStr, "cached", time.Since(start))
		}
		logutil.Logger.Debugf("ResolverWithCache: cache hit for key=%s", key)
		logutil.Logger.Debug("ResolverWithCache: end (cache hit)")
		return answers
	}

	if EnableMetrics {
		metric.MetricsRecorderInstance.RecordCacheMiss()
	}

	var answers []dns.RR
	var resolver func(string, uint16, string) []dns.RR
	resolver = dnsresolver.ResolverForClient
	if EnableDomainRouting {
		if _, ok := domainrouting.RoutingTable[domain]; ok {
			resolver = dnsresolver.ResolverForDomain
		}
	}
	answers = resolver(domain, qtype, clientIP)
	status := "success"
	if len(answers) == 0 {
		status = "nxdomain"
		go SaveToCache(key, answers, DefaultDNSCacheTTL/config.NegativeResponseTTLDivisor)
		if EnableMetrics {
			metric.MetricsRecorderInstance.RecordDNSQuery(qTypeStr, status, time.Since(start))
		}
		logutil.Logger.Debugf("ResolverWithCache: no answers for key=%s, status=%s", key, status)
		logutil.Logger.Debug("ResolverWithCache: end (nxdomain)")
		return answers
	}

	minTTL := uint32(^uint32(0))
	useDefaultTTL := false

	for i := range answers {
		rr := answers[i]
		ttl := rr.Header().Ttl
		if ttl < minTTL {
			minTTL = ttl
		}
		switch v := rr.(type) {
		case *dns.A:
			if v.A.String() == config.ARecordInvalidAnswer {
				useDefaultTTL = true
			}
		case *dns.AAAA:
			if v.AAAA.String() == config.AAAARecordInvalidAnswer {
				useDefaultTTL = true
			}
		}
		if useDefaultTTL {
			break
		}
	}
	ttl := DefaultDNSCacheTTL
	if !useDefaultTTL && minTTL > 0 {
		ttlDuration := time.Duration(minTTL) * time.Second
		if ttlDuration < config.MinCacheTTL {
			ttl = config.MinCacheTTL
		} else if ttlDuration > config.MaxCacheTTL {
			ttl = config.MaxCacheTTL
		} else {
			ttl = ttlDuration
		}
	}
	go SaveToCache(key, answers, ttl)

	if EnableMetrics {
		metric.MetricsRecorderInstance.RecordDNSQuery(qTypeStr, status, time.Since(start))
	}
	logutil.Logger.Debugf("ResolverWithCache: resolved and cached key=%s, ttl=%v", key, ttl)
	logutil.Logger.Debug("ResolverWithCache: end (resolved)")
	return answers
}

func StartCacheStatsLogger() {
	logutil.Logger.Debug("StartCacheStatsLogger: start")
	ticker := time.NewTicker(config.DefaultDNSStatslog)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			hits := atomic.LoadInt64(&cacheHits)
			requests := atomic.LoadInt64(&cacheRequests)
			hitPct := 0.0
			if requests > 0 {
				hitPct = (float64(hits) / float64(requests)) * 100
			}
			logutil.Logger.Infof("Requests: %d, Hits: %d, Hit Rate: %.2f%%, Miss Rate: %.2f%%",
				requests, hits, hitPct, 100-hitPct)
			if DnsCache != nil {
				items := DnsCache.Items()
				itemCount := len(items)
				if itemCount == 0 {
					logutil.Logger.Warn("No cache entries found")
				} else {
					logutil.Logger.Infof("Cache Entries: %d", itemCount)
					if EnableMetrics {
						metric.MetricsRecorderInstance.UpdateCacheSize(itemCount)
					}
				}
			}
			logutil.Logger.Debug("StartCacheStatsLogger: tick end")
		}
	}()
	logutil.Logger.Debug("StartCacheStatsLogger: end (goroutine started)")
}
