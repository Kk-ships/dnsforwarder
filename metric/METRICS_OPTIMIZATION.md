# DNS Metrics Performance Optimization

This document outlines the strategies implemented to ensure that metric collection does not hamper the `ServeDNS` function performance.

## Performance Optimizations Implemented

### 1. Fast Metrics Always Enabled

**Implementation**: The DNS forwarder now always uses optimized fast metrics for minimal performance impact.

**Benefits**:

- Atomic counters for immediate updates (1-5μs overhead)
- Batched Prometheus metric updates to reduce lock contention
- Non-blocking operations in DNS query hot path
- Pre-allocated structures to minimize memory allocations

### 2. Asynchronous Metric Processing

**Implementation**: Metrics are processed in background goroutines with configurable batching.

**Features**:

- Configurable batch sizes and delays
- Graceful degradation when buffers are full
- No blocking operations in DNS response path

### 3. Optimized Memory Usage

**Implementation**: Reduced allocations and efficient data structures.

**Optimizations**:

- Pre-allocated label arrays for common cases
- Object pooling for metric events
- Minimal string allocations in hot path

## Configuration Options

### Environment Variables

```bash
# Fast metrics are always enabled for optimal performance

# Batch size for metric updates (default: 500)
METRICS_BATCH_SIZE=500

# Delay between metric batches (default: 100ms)
METRICS_BATCH_DELAY=100ms

# Buffer size for metric channel (default: 10000)
METRICS_CHANNEL_SIZE=10000
```

### Fast Metrics Implementation

The DNS forwarder now exclusively uses fast metrics which provide:

- Atomic counters for immediate metric updates
- Batched Prometheus updates every 100ms (configurable)
- Non-blocking operations in DNS query path
- Minimal memory allocations and GC pressure
- Graceful degradation when metric buffers are full

## Performance Characteristics

### Optimized Implementation (Fast Metrics Always Enabled)

- ~1-5μs additional latency per DNS query
- Atomic increments with batched Prometheus updates
- No blocking operations in DNS hot path
- Minimal memory allocations and GC pressure
- Configurable batching for optimal performance tuning

## Monitoring Metrics Performance

### Key Metrics to Monitor

- `dns_query_duration_seconds`: Should show minimal increase
- DNS server goroutine count: Should remain stable
- Memory usage: Should not increase significantly
- Metric channel buffer usage (internal counter)

### Performance Testing

```bash
# Test DNS query performance
dig @localhost example.com

# Load testing with metrics enabled/disabled
# Compare latency and throughput metrics
```

## Implementation Details

### Fast Metrics Architecture

1. **Atomic Counters**: Immediate increment for high-frequency metrics
2. **Batched Updates**: Periodic flush to Prometheus metrics (100ms)
3. **Non-blocking Channels**: Graceful degradation when buffers full
4. **Pre-allocated Structures**: Minimize GC pressure

### Error Handling

- Channel buffer overflow: Metrics are dropped (logged)
- Batch processing errors: Logged but don't affect DNS service
- Fallback mechanisms: Standard metrics if fast metrics fail

## Best Practices

1. **Fast Metrics Always Enabled**: The system automatically uses optimized metrics
2. **Monitor Buffer Usage**: Ensure metric channels don't overflow
3. **Tune Batch Settings**: Adjust `METRICS_BATCH_SIZE` and `METRICS_BATCH_DELAY` based on load
4. **Test Performance**: Benchmark with/without metrics enabled
5. **Monitor Resource Usage**: Watch CPU and memory impact

## Troubleshooting

### High Metric Latency

- Increase `METRICS_BATCH_SIZE`
- Decrease `METRICS_BATCH_DELAY`
- Check for lock contention in Prometheus metrics

### Memory Issues

- Reduce `METRICS_CHANNEL_SIZE`
- Enable more aggressive batching
- Monitor for metric buffer overflows

### DNS Query Latency

- Verify fast metrics are enabled
- Check for blocking operations in metric code
- Monitor goroutine count and stack traces
