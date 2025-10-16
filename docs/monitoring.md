# Monitoring and Metrics

## Logging & Stats
- Logs are written to stdout and kept in a ring buffer for diagnostics.
- Periodic logs show DNS usage and cache hit/miss rates.
- When client routing is enabled, logs show which servers are being used for each client.
- Client routing decisions and server health status are logged for troubleshooting.

## Prometheus Metrics
When `ENABLE_METRICS=true`, the following metrics are available at `/metrics` endpoint:

### DNS Query Metrics
- `dns_queries_total` - Total DNS queries processed (by type and status)
- `dns_query_duration_seconds` - DNS query duration histogram

### Cache Metrics
- `dns_cache_hits_total` - Total cache hits
- `dns_cache_misses_total` - Total cache misses
- `dns_cache_size` - Current cache size

### Upstream Server Metrics
- `dns_upstream_queries_total` - Queries sent to upstream servers
- `dns_upstream_query_duration_seconds` - Upstream query duration
- `dns_upstream_servers_reachable` - Server reachability status
- `dns_upstream_servers_total` - Total configured servers

### System Metrics
- `dns_server_uptime_seconds_total` - Server uptime
- `dns_server_memory_usage_bytes` - Memory usage
- `dns_server_goroutines` - Active goroutines

### Error Metrics
- `dns_errors_total` - Total DNS errors (by error type and source)

### Device IP and Domain Metrics
- `dns_device_ip_queries_total` - Total DNS queries per device IP
- `dns_domain_queries_total` - Total DNS queries per domain (by domain and status)
- `dns_domain_hits_total` - Total hits per domain

## Health Check Endpoints
- `/health` - Simple health check (returns "OK")
- `/status` - JSON status response

## Example Prometheus Configuration
```yaml
scrape_configs:
  - job_name: 'dns-forwarder'
    static_configs:
      - targets: ['localhost:8080']
    metrics_path: '/metrics'
    scrape_interval: 30s
```
