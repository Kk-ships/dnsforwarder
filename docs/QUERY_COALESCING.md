# Query Coalescing

Query coalescing is an optimization technique that combines identical in-flight DNS requests to reduce upstream DNS server load and improve response times.

## Overview

When multiple clients request the same DNS record simultaneously, instead of sending multiple identical queries to upstream DNS servers, the DNS forwarder:

1. Groups identical requests together
2. Sends only one upstream query
3. Returns the same response to all waiting clients

This significantly reduces upstream DNS server load and can improve response times for duplicate queries.

## Benefits

- **Reduced Upstream Load**: Eliminates duplicate queries to upstream DNS servers
- **Improved Performance**: Subsequent identical requests get responses faster by waiting for the first query
- **Resource Efficiency**: Reduces network bandwidth and server resources
- **Better Caching**: Works alongside DNS caching for optimal performance

## Configuration

Query coalescing is **enabled by default** and can be configured using environment variables:

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ENABLE_QUERY_COALESCING` | `true` | Enable/disable query coalescing |
| `QUERY_COALESCING_TIMEOUT` | `5s` | Maximum time to wait for coalesced query |
| `QUERY_COALESCING_CLEANUP_INTERVAL` | `1m` | How often to clean up stale queries |

### Example Configuration

```bash
# Disable query coalescing
ENABLE_QUERY_COALESCING=false

# Reduce timeout for faster fallback
QUERY_COALESCING_TIMEOUT=2s

# More frequent cleanup
QUERY_COALESCING_CLEANUP_INTERVAL=30s
```

## How It Works

1. **Request Arrives**: A DNS query arrives for `example.com A`
2. **Check In-Flight**: System checks if an identical query is already being processed
3. **Two Scenarios**:
   - **New Query**: If no identical query exists, send upstream query and wait for response
   - **Existing Query**: If identical query exists, wait for the existing query to complete

### Example Scenario

```
Time    Client    Action                    Upstream Queries
----    ------    ------                    ----------------
0ms     Client1   Query: example.com A      → Upstream query sent
5ms     Client2   Query: example.com A      → Waits for Client1's query
10ms    Client3   Query: example.com A      → Waits for Client1's query
50ms    Response  A: 192.168.1.1           ← Response received
51ms    All       Receive: 192.168.1.1     → All clients get response
```

**Result**: 1 upstream query instead of 3, all clients served in ~51ms

## Query Key Generation

Queries are considered identical based on:
- Domain name
- Query type (A, AAAA, CNAME, etc.)
- Client IP (if client routing is enabled)

### Key Examples

- Standard: `example.com:1` (A record)
- With client routing: `example.com:1:192.168.1.100`
- AAAA record: `example.com:28`

## Monitoring

### Prometheus Metrics

Query coalescing effectiveness can be monitored using:

```prometheus
# Total coalesced queries (queries that didn't hit upstream)
dns_query_coalesced_total

# Compare with total upstream queries
dns_upstream_queries_total
```

### Log Messages

```
DEBUG Query coalesced for key: example.com:1 (waiters: 3)
DEBUG Query result broadcast for key: example.com:1 (waiters: 3)
```

## Edge Cases and Timeouts

### Timeout Handling

If a coalesced query times out:
1. Waiting clients receive a timeout warning
2. Each client falls back to executing their own direct query
3. This ensures availability even if the first query hangs

### Cache Integration

Query coalescing works seamlessly with DNS caching:
1. Check cache first (instant response if cached)
2. If cache miss, use query coalescing for upstream query
3. Cache the response for future requests

## Performance Impact

### Positive Impact
- Reduced upstream DNS queries (can reduce by 50-90% in high-traffic scenarios)
- Lower network bandwidth usage
- Reduced upstream server load
- Faster responses for duplicate queries

### Minimal Overhead
- Small memory footprint for tracking in-flight queries
- Minimal CPU overhead for query key generation
- Automatic cleanup prevents memory leaks

## Troubleshooting

### Common Issues

1. **High timeout warnings**
   - Solution: Increase `QUERY_COALESCING_TIMEOUT`
   - Check upstream DNS server performance

2. **Memory usage concerns**
   - Monitor in-flight query count
   - Adjust `QUERY_COALESCING_CLEANUP_INTERVAL`

3. **Disable for debugging**
   - Set `ENABLE_QUERY_COALESCING=false`
   - Isolate query coalescing from other issues

### Debug Information

Enable debug logging to see query coalescing in action:
```bash
LOG_LEVEL=debug
```

Look for log messages containing:
- "Query coalesced for key"
- "Query result broadcast for key"
- "Cleaned up stale query"

## Best Practices

1. **Keep defaults**: The default configuration works well for most use cases
2. **Monitor metrics**: Use Prometheus metrics to track effectiveness
3. **Tune timeout**: Adjust timeout based on upstream DNS server performance
4. **Consider client routing**: If using client-specific routing, understand the impact on coalescing effectiveness

## Related Features

Query coalescing works best when combined with:
- **DNS Caching**: Reduces the need for queries altogether
- **Stale Cache Updates**: Keeps cache fresh in the background
- **Client Routing**: Routes queries to optimal DNS servers
- **Domain Routing**: Routes specific domains to designated servers
