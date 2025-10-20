# Configuration

You can configure the forwarder using environment variables or a `.env` file. Example `.env`:

```
PRIVATE_DNS_SERVERS=192.168.1.1:53,192.168.1.2:53
PUBLIC_DNS_SERVERS=1.1.1.1:53,8.8.8.8:53
CACHE_SERVERS_TTL=10s
DNS_TIMEOUT=5s
WORKER_COUNT=5
TEST_DOMAIN=google.com
DNS_PORT=:53
UDP_SIZE=65535
DNS_STATSLOG=1m
CACHE_SIZE=10000
DNS_CACHE_TTL=30m

# Metric Configuration (Optional)
ENABLE_METRICS=true
METRICS_PORT=:8080
METRICS_PATH=/metrics
METRICS_UPDATE_INTERVAL=30s

# Client-Based DNS Routing (Optional)
ENABLE_CLIENT_ROUTING=true
PUBLIC_ONLY_CLIENTS=192.168.1.100,10.0.0.50
PUBLIC_ONLY_CLIENT_MACS=00:11:22:33:44:55,AA:BB:CC:DD:EE:FF

# Domain Routing (Optional)
ENABLE_DOMAIN_ROUTING=true
DOMAIN_ROUTING_FOLDER=/etc/dnsforwarder/domain-routes
DOMAIN_ROUTING_TABLE_RELOAD_INTERVAL=60

# Cache Persistence (Optional)
ENABLE_CACHE_PERSISTENCE=true
CACHE_PERSISTENCE_FILE=/app/cache/dns_cache.json
CACHE_PERSISTENCE_INTERVAL=5m
CACHE_PERSISTENCE_MAX_AGE=1h

# Logger Configuration
LOG_LEVEL=info
```

## Basic Configuration
- **PRIVATE_DNS_SERVERS:** Comma-separated list of private DNS servers (e.g., PiHole, AdGuard Home).
- **PUBLIC_DNS_SERVERS:** Comma-separated list of public DNS servers (e.g., Cloudflare, Google).
- **CACHE_SERVERS_REFRESH:** Refresh interval of probing healthy servers.
- **DNS_TIMEOUT:** Timeout for DNS queries to upstream servers.
- **WORKER_COUNT:** Number of concurrent health checks for upstream servers.
- **TEST_DOMAIN** Test domain to check DNS resolution (default `google.com`).
- **DNS_PORT:** Port to listen on (default `:53`).
- **UDP_SIZE:** Maximum UDP packet size (default `65535`).
- **DNS_STATSLOG:** Interval for logging DNS statistics (default `1m`).
- **CACHE_SIZE:** Maximum number of DNS entries to cache.
- **DNS_CACHE_TTL:** How long to cache DNS responses if not specified by upstream servers (default `30m`).


## Metric Configuration
- **ENABLE_METRICS:** Enable Prometheus metrics (default `true`).
- **METRICS_PORT:** Port for Prometheus metrics endpoint (default `:8080`).
- **METRICS_PATH:** Path for metrics endpoint (default `/metrics`).
- **METRICS_UPDATE_INTERVAL:** How often to update system metrics (default `30s`).

## Client-Based DNS Routing Configuration
- **ENABLE_CLIENT_ROUTING:** Enable client-based DNS routing (default `false`).
- **PUBLIC_ONLY_CLIENTS:** Comma-separated list of client IPs that should only use public DNS servers.
- **PUBLIC_ONLY_CLIENT_MACS** Comma-separated list of client MAC addresses that should only use public DNS servers.

## Domain Routing Configuration
- **ENABLE_DOMAIN_ROUTING:** Enable domain routing (default `false`).
- **DOMAIN_ROUTING_FOLDER:** Comma-separated list of folders containing routing configuration files
- **DOMAIN_ROUTING_TABLE_RELOAD_INTERVAL** Domain routing reload interval in seconds

## Cache Persistence Configuration
- **ENABLE_CACHE_PERSISTENCE:** Enable cache persistence to disk for hot starts (default `true`).
- **CACHE_PERSISTENCE_FILE:** Path to the cache persistence file (default `/app/cache/dns_cache.json`).
- **CACHE_PERSISTENCE_INTERVAL:** How often to save cache to disk (default `5m`).
- **CACHE_PERSISTENCE_MAX_AGE:** Maximum age of cache file before it's considered stale and ignored (default `1h`).

## Stale Cache Updater Configuration
- **ENABLE_STALE_UPDATER:** Enable proactive updates of frequently accessed cache entries before they expire (default `true`).
- **STALE_UPDATE_THRESHOLD:** How close to expiry (time remaining) before an entry is considered stale and updated (default `2m`).
- **STALE_UPDATE_INTERVAL:** How often to check for stale entries that need updating (default `30s`).
- **STALE_UPDATE_MIN_ACCESS_COUNT:** Minimum number of times an entry must be accessed to qualify for stale updates (default `5`).
- **STALE_UPDATE_MAX_CONCURRENT:** Maximum number of concurrent stale update operations (default `10`).
