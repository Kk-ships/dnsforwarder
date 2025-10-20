# Stale Cache Updater

The Stale Cache Updater is a proactive caching feature that automatically refreshes frequently accessed cache entries before they expire. This ensures that popular domains always have fresh DNS responses without causing client-facing delays.

## How It Works
- The system tracks access patterns for all cached DNS entries, recording access counts and timestamps.
- Periodically (based on `STALE_UPDATE_INTERVAL`), the updater checks for cache entries that are:
  1. **Frequently accessed** (access count >= `STALE_UPDATE_MIN_ACCESS_COUNT`)
  2. **Close to expiring** (time until expiry <= `STALE_UPDATE_THRESHOLD`)
- These entries are refreshed in the background by re-querying the upstream DNS servers.
- Updates are performed concurrently with a limit set by `STALE_UPDATE_MAX_CONCURRENT` to prevent overwhelming upstream servers.
- Access tracking information is persisted along with cache entries for continuity across restarts.

## Benefits
- **Zero Client Impact:** Popular domains are refreshed before expiration, eliminating cache miss delays for frequently requested domains.
- **Improved Performance:** Reduces overall DNS resolution times for commonly accessed domains.
- **Smart Resource Usage:** Only updates entries that are actually being used frequently.
- **Configurable Behavior:** All thresholds and intervals can be tuned based on your traffic patterns.
- **Metrics Integration:** Stale updates are tracked in Prometheus metrics for monitoring and optimization.

## Example Configuration
Add to your `.env` file:
```
ENABLE_STALE_UPDATER=true
STALE_UPDATE_THRESHOLD=2m          # Update entries within 2 minutes of expiry
STALE_UPDATE_INTERVAL=30s          # Check for stale entries every 30 seconds
STALE_UPDATE_MIN_ACCESS_COUNT=5    # Only update entries accessed 5+ times
STALE_UPDATE_MAX_CONCURRENT=10     # Maximum 10 concurrent updates
```

## Use Cases
- **High-traffic DNS servers** where popular domains should always have cached responses
- **Corporate environments** where internal services and frequently accessed external domains need consistent performance
- **CDN and edge deployments** where cache miss latency directly impacts user experience
- **Gaming and streaming services** where DNS resolution delays can affect performance

## Monitoring
- Stale update operations are logged with detailed information about which entries are being refreshed
- Prometheus metrics track stale update success/failure rates and timing
- Access patterns and update frequency can be monitored through the metrics endpoint

## Troubleshooting Cache Persistence Issues
If you see "permission denied" errors for cache persistence:
1. **Docker/Container**: Ensure the cache directory is properly mounted and writable:
   ```yaml
   volumes:
     - ./cache-data/:/app/cache/:rw
   ```
2. **File Path**: Use the `CACHE_PERSISTENCE_FILE` environment variable to specify a writable location:
   ```bash
   CACHE_PERSISTENCE_FILE=/app/cache/dns_cache.json
   ```
3. **Disable if needed**: Set `ENABLE_CACHE_PERSISTENCE=false` to disable persistence entirely.
4. **Alternative paths**: The application will automatically try alternative paths (`/tmp`, `./`) if the primary path fails.
