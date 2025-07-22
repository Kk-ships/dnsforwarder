package cache

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"dnsloadbalancer/config"
	"dnsloadbalancer/logutil"
	"dnsloadbalancer/metric"

	"github.com/miekg/dns"
	"github.com/patrickmn/go-cache"
)

type cacheEntry struct {
	Answers []dns.RR
}

const aRecordInvalidAnswer = "0.0.0.0"
const aaaRecordInvalidAnswer = "::"
const negativeResponseTTLDivisor = 4

var (
	cacheHits     int64
	cacheRequests int64
	DnsCache      *cache.Cache
)

var (
	DefaultDNSCacheTTL  time.Duration = 30 * time.Minute
	EnableMetrics       bool
	EnableClientRouting bool
)

func Init(defaultDNSCacheTTL time.Duration, enableMetrics bool, _ interface{}, enableClientRouting bool) {
	DnsCache = cache.New(defaultDNSCacheTTL, 2*defaultDNSCacheTTL)
	DefaultDNSCacheTTL = defaultDNSCacheTTL
	EnableMetrics = enableMetrics
	EnableClientRouting = enableClientRouting
}

func CacheKey(domain string, qtype uint16) string {
	var b strings.Builder
	b.Grow(len(domain) + 1 + 5)
	b.WriteString(domain)
	b.WriteByte(':')
	b.WriteString(strconv.FormatUint(uint64(qtype), 10))
	return b.String()
}

func SaveToCache(key string, answers []dns.RR, ttl time.Duration) {
	DnsCache.Set(key, answers, ttl)
}

func LoadFromCache(key string) ([]dns.RR, bool) {
	val, found := DnsCache.Get(key)
	if !found {
		return nil, false
	}
	answers, ok := val.([]dns.RR)
	if !ok {
		return nil, false
	}
	return answers, true
}

func ResolverWithCache(domain string, qtype uint16, resolver func(string, uint16) []dns.RR, resolverForClient func(string, uint16, string) []dns.RR, clientIP string) []dns.RR {
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
		return answers
	}

	if EnableMetrics {
		metric.MetricsRecorderInstance.RecordCacheMiss()
	}

	var answers []dns.RR
	if clientIP != "" && EnableClientRouting {
		answers = resolverForClient(domain, qtype, clientIP)
	} else {
		answers = resolver(domain, qtype)
	}

	status := "success"
	if len(answers) == 0 {
		status = "nxdomain"
		go SaveToCache(key, answers, DefaultDNSCacheTTL/negativeResponseTTLDivisor)
		if EnableMetrics {
			metric.MetricsRecorderInstance.RecordDNSQuery(qTypeStr, status, time.Since(start))
		}
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
			if v.A.String() == aRecordInvalidAnswer {
				useDefaultTTL = true
			}
		case *dns.AAAA:
			if v.AAAA.String() == aaaRecordInvalidAnswer {
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
		const minCacheTTL = 30 * time.Second
		const maxCacheTTL = 24 * time.Hour
		if ttlDuration < minCacheTTL {
			ttl = minCacheTTL
		} else if ttlDuration > maxCacheTTL {
			ttl = maxCacheTTL
		} else {
			ttl = ttlDuration
		}
	}
	go SaveToCache(key, answers, ttl)

	if EnableMetrics {
		metric.MetricsRecorderInstance.RecordDNSQuery(qTypeStr, status, time.Since(start))
	}
	return answers
}

func StartCacheStatsLogger() {
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
			logutil.LogWithBufferf("Requests: %d, Hits: %d, Hit Rate: %.2f%%, Miss Rate: %.2f%%",
				requests, hits, hitPct, 100-hitPct)
			if DnsCache != nil {
				items := DnsCache.Items()
				itemCount := len(items)
				if itemCount == 0 {
					logutil.LogWithBufferf("No cache entries found")
				} else {
					logutil.LogWithBufferf("Cache Entries: %d", itemCount)
					if EnableMetrics {
						metric.MetricsRecorderInstance.UpdateCacheSize(itemCount)
					}
				}
			}
		}
	}()
}
