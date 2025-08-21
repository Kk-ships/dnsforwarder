# DNS Rate Limiting Feature

This document describes the comprehensive rate limiting feature implemented for the DNS forwarder.

## Overview

The rate limiting system provides protection against DNS query abuse, DDoS attacks, and ensures fair resource utilization among clients. It features:

- **Per-client rate limiting** with sliding windows
- **Adaptive throttling** based on behavioral analysis
- **Suspicious behavior detection** and automatic blocking
- **Comprehensive metrics** and monitoring
- **RESTful API** for management and monitoring
- **Graceful degradation** with configurable responses

## Features

### 1. Multi-Level Rate Limiting

- **Per-second limits**: Immediate burst protection
- **Per-minute limits**: Short-term abuse prevention
- **Per-hour limits**: Long-term fair usage enforcement
- **Sliding windows**: Smooth rate limiting without sudden resets

### 2. Behavioral Analysis

- **Burst detection**: Identifies unusual traffic spikes
- **Pattern analysis**: Detects sustained high-rate queries
- **Suspicion scoring**: Gradual escalation system (0-100)
- **Adaptive response**: Dynamic rate adjustments based on behavior

### 3. Intelligent Blocking

- **Automatic blocking**: Clients exceeding suspicion thresholds
- **Temporary blocks**: Configurable duration with automatic expiration
- **Manual override**: API endpoints for emergency unblocking
- **Reason tracking**: Detailed logging of block reasons

### 4. Monitoring & Observability

- **Prometheus metrics**: Comprehensive rate limiting statistics
- **Real-time API**: Live statistics and client information
- **Structured logging**: Detailed audit trail
- **Health checks**: Service status and configuration

## Configuration

### Environment Variables

```bash
# Basic Rate Limiting
ENABLE_RATE_LIMIT=true                    # Enable/disable rate limiting
RATE_LIMIT_QPS=100                        # Max queries per second per client
RATE_LIMIT_QPM=3000                       # Max queries per minute per client
RATE_LIMIT_QPH=50000                      # Max queries per hour per client

# Sliding Window Configuration
RATE_LIMIT_WINDOW_SIZE=60s                # Size of sliding window
RATE_LIMIT_WINDOW_SLOTS=60                # Number of time slots in window

# Burst Detection
RATE_LIMIT_BURST_THRESHOLD=200            # QPS threshold for burst detection
RATE_LIMIT_BURST_WINDOW=5s                # Time window for burst analysis
RATE_LIMIT_MAX_BURSTS_PER_MIN=5           # Max allowed bursts per minute

# Adaptive Throttling
RATE_LIMIT_ADAPTIVE_ENABLED=true          # Enable adaptive throttling
RATE_LIMIT_SUSPICION_THRESHOLD=30         # Suspicion level to start throttling
RATE_LIMIT_THROTTLE_MULTIPLIER=0.5        # Rate reduction factor (50% in this case)

# Automatic Blocking
RATE_LIMIT_BLOCKING_ENABLED=true          # Enable automatic blocking
RATE_LIMIT_BLOCK_THRESHOLD=80             # Suspicion level to trigger blocking
RATE_LIMIT_BLOCK_DURATION=5m              # How long to block clients

# Maintenance
RATE_LIMIT_CLEANUP_INTERVAL=5m            # Cleanup old client data interval
RATE_LIMIT_CLIENT_TIMEOUT=30m             # Timeout for inactive client tracking
```

### Default Configuration

The system ships with sensible defaults suitable for most environments:

- **100 QPS / 3,000 QPM / 50,000 QPH** per client
- **Adaptive throttling** enabled with 50% rate reduction
- **5-minute blocks** for highly suspicious clients
- **Automatic cleanup** of old client data

## API Endpoints

When metrics are enabled, the following endpoints are available:

### Global Statistics
```
GET /api/ratelimit/stats
```

Response:
```json
{
  "success": true,
  "data": {
    "total_requests": 150000,
    "blocked_requests": 1250,
    "allowed_requests": 148750,
    "active_clients": 45,
    "blocked_clients": 2,
    "suspicious_clients": 8,
    "config": {
      "max_qps": 100,
      "max_qpm": 3000,
      "max_qph": 50000,
      "burst_threshold": 200,
      "block_duration": "5m0s"
    }
  }
}
```

### Client Statistics
```
GET /api/ratelimit/client/{ip}
```

Response:
```json
{
  "success": true,
  "data": {
    "exists": true,
    "total_requests": 5420,
    "burst_count": 3,
    "suspicion_level": 25,
    "is_blocked": false,
    "blocked_until": "0001-01-01T00:00:00Z",
    "block_reason": "",
    "last_burst_time": "2025-08-21T10:15:30Z",
    "last_update": "2025-08-21T10:20:15Z"
  }
}
```

### Unblock Client
```
DELETE /api/ratelimit/client/{ip}
```

Response:
```json
{
  "success": true,
  "message": "Client 192.168.1.100 unblocked successfully"
}
```

### Configuration
```
GET /api/ratelimit/config
```

Response:
```json
{
  "success": true,
  "data": {
    "max_qps": 100,
    "max_qpm": 3000,
    "max_qph": 50000,
    "burst_threshold": 200,
    "adaptive_enabled": true,
    "blocking_enabled": true,
    "block_duration": "5m0s",
    "window_size": "1m0s",
    "cleanup_interval": "5m0s"
  }
}
```

## Prometheus Metrics

### Rate Limiting Metrics

```prometheus
# Blocked requests counter
dns_rate_limit_blocked_total{client_ip, reason}

# Allowed requests counter
dns_rate_limit_allowed_total{client_ip}

# Suspicious clients gauge
dns_rate_limit_suspicious_clients{client_ip}
```

### Example Queries

```prometheus
# Rate limit block rate
rate(dns_rate_limit_blocked_total[5m])

# Top suspicious clients
topk(10, dns_rate_limit_suspicious_clients)

# Block reasons breakdown
sum by (reason) (dns_rate_limit_blocked_total)

# Rate limit effectiveness
dns_rate_limit_allowed_total / (dns_rate_limit_allowed_total + dns_rate_limit_blocked_total) * 100
```

## Behavior Examples

### Normal Operation
- Client sends 50 QPS → **All queries allowed**
- Metrics show normal patterns → **No throttling**
- No suspicious activity → **Suspicion level: 0**

### Burst Detection
- Client sends 250 QPS for 2 seconds → **Burst detected**
- Suspicion level increases to 10 → **Still allowed**
- Pattern continues → **Suspicion level: 30** → **Throttling to 50 QPS**

### Blocking Scenario
- Client sends 500 QPS continuously → **Multiple bursts detected**
- Suspicion level reaches 80 → **Client blocked for 5 minutes**
- All queries return SERVFAIL → **Block reason logged**
- After 5 minutes → **Automatic unblock** → **Suspicion reset**

### Adaptive Recovery
- Blocked client resumes normal behavior → **Suspicion slowly decreases**
- No suspicious activity for 5 minutes → **Suspicion: 70**
- Continued good behavior → **Full recovery to suspicion: 0**

## Testing

### Manual Testing

Use the included test client to verify rate limiting:

```bash
# Build the test client
go build -o tools/rate-limit-test tools/test_client.go

# Test with moderate load (should work)
./tools/rate-limit-test -clients=5 -qps=20 -duration=30s

# Test with high load (should trigger rate limiting)
./tools/rate-limit-test -clients=10 -qps=150 -duration=30s

# Test with extreme load (should trigger blocking)
./tools/rate-limit-test -clients=1 -qps=500 -duration=60s
```

### Expected Results

**Moderate Load (5 clients × 20 QPS = 100 total QPS)**:
- All queries should succeed
- No rate limiting observed
- Suspicion levels remain low

**High Load (10 clients × 150 QPS = 1500 total QPS)**:
- Rate limiting should activate after ~100 QPS per client
- Some queries return SERVFAIL
- Burst detection triggers
- Adaptive throttling may activate

**Extreme Load (1 client × 500 QPS)**:
- Immediate burst detection
- Rapid suspicion level increase
- Client blocking after sustained abuse
- All queries blocked with SERVFAIL

### Monitoring During Tests

Watch the metrics during testing:

```bash
# Monitor global stats
curl http://localhost:8080/api/ratelimit/stats | jq

# Monitor specific client
curl http://localhost:8080/api/ratelimit/client/192.168.1.100 | jq

# View Prometheus metrics
curl http://localhost:8080/metrics | grep rate_limit
```

## Production Deployment

### Recommended Settings

**Small Environment (< 100 clients)**:
```bash
RATE_LIMIT_QPS=200
RATE_LIMIT_QPM=6000
RATE_LIMIT_BURST_THRESHOLD=300
```

**Medium Environment (100-1000 clients)**:
```bash
RATE_LIMIT_QPS=100
RATE_LIMIT_QPM=3000
RATE_LIMIT_BURST_THRESHOLD=200
```

**Large Environment (> 1000 clients)**:
```bash
RATE_LIMIT_QPS=50
RATE_LIMIT_QPM=1500
RATE_LIMIT_BURST_THRESHOLD=100
RATE_LIMIT_BLOCK_DURATION=10m
```

### Monitoring Alerts

Set up Prometheus alerts for:

```yaml
- alert: HighRateLimitBlockRate
  expr: rate(dns_rate_limit_blocked_total[5m]) > 10
  for: 2m
  annotations:
    summary: "High rate of DNS queries being blocked"

- alert: SuspiciousClientsDetected
  expr: count(dns_rate_limit_suspicious_clients > 50) > 5
  for: 1m
  annotations:
    summary: "Multiple suspicious clients detected"
```

### Grafana Dashboard

Create dashboards showing:
- Rate limiting effectiveness over time
- Top clients by query volume
- Block reasons breakdown
- Suspicious client trends
- System impact metrics

## Troubleshooting

### Common Issues

**Rate limiting too aggressive**:
- Increase QPS/QPM limits
- Raise suspicion thresholds
- Reduce throttling multiplier

**Rate limiting not working**:
- Verify `ENABLE_RATE_LIMIT=true`
- Check DNS server startup logs
- Test with extreme load
- Monitor API endpoints

**False positives**:
- Review burst thresholds
- Adjust adaptive settings
- Implement client whitelisting (future feature)

**Performance impact**:
- Monitor metrics overhead
- Adjust cleanup intervals
- Consider client timeout settings

### Debug Mode

Enable debug logging to see detailed rate limiting decisions:

```bash
LOG_LEVEL=debug
```

This will show:
- Individual rate limit checks
- Suspicion level changes
- Block/unblock decisions
- Burst detection events

## Future Enhancements

Potential future features include:

1. **Client Whitelisting**: Exempt trusted clients from rate limiting
2. **Geographic Rate Limiting**: Different limits by region
3. **Query Type Filtering**: Different limits for different DNS record types
4. **Machine Learning**: Advanced behavioral analysis
5. **Integration with Threat Intelligence**: Block known malicious IPs
6. **Custom Response Codes**: More specific DNS error responses

## Security Considerations

- Rate limiting helps prevent DNS amplification attacks
- Behavioral analysis detects sophisticated abuse patterns
- Automatic blocking provides rapid response to threats
- API endpoints should be secured in production environments
- Log analysis can identify attack patterns and improve defenses

The rate limiting system provides a robust foundation for DNS service protection while maintaining excellent performance for legitimate users.
