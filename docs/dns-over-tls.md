# DNS-over-TLS (DoT) Support

DNS-over-TLS provides encrypted DNS queries to upstream servers, protecting your DNS traffic from eavesdropping and tampering.

## Overview

When enabled, the DNS forwarder can send queries to upstream DNS servers over TLS (port 853) instead of plain UDP (port 53). This adds a layer of security and privacy to your DNS resolution.

## Configuration

Enable DoT by setting the following environment variables:

```bash
ENABLE_DOT=true
DOT_SERVERS=1.1.1.1:853,8.8.8.8:853
DOT_SERVER_NAME=cloudflare-dns.com
DOT_SKIP_VERIFY=false
DOT_FALLBACK_TO_UDP=true
```

### Configuration Options

- **ENABLE_DOT** (default: `false`)
  - Set to `true` to enable DNS-over-TLS support

- **DOT_SERVERS** (default: empty)
  - Comma-separated list of DoT server addresses with port 853
  - Example: `1.1.1.1:853,8.8.8.8:853,9.9.9.9:853`
  - These servers will be queried using TLS encryption

- **DOT_SERVER_NAME** (default: empty)
  - Server name (SNI) for TLS certificate validation
  - Required for proper certificate verification
  - Examples: `cloudflare-dns.com`, `dns.google`, `dns.quad9.net`
  - **Required when ENABLE_DOT is true**; must be set for proper certificate verification
  - Do not leave empty—using the IP address is not supported and will result in a configuration error

- **DOT_SKIP_VERIFY** (default: `false`)
  - Skip TLS certificate verification
  - **WARNING**: Only enable for testing/debugging
  - Setting to `true` makes connections vulnerable to man-in-the-middle attacks

- **DOT_FALLBACK_TO_UDP** (default: `true`)
  - Automatically fallback to regular UDP if DoT connection fails
  - Ensures reliability when DoT servers are unreachable
  - Set to `false` to require DoT (queries will fail if TLS connection fails)

## Popular DoT Providers

### Cloudflare DNS
```bash
DOT_SERVERS=1.1.1.1:853,1.0.0.1:853
DOT_SERVER_NAME=cloudflare-dns.com
```

### Google Public DNS
```bash
DOT_SERVERS=8.8.8.8:853,8.8.4.4:853
DOT_SERVER_NAME=dns.google
```

### Quad9
```bash
DOT_SERVERS=9.9.9.9:853,149.112.112.112:853
DOT_SERVER_NAME=dns.quad9.net
```

### AdGuard DNS
```bash
DOT_SERVERS=94.140.14.14:853,94.140.15.15:853
DOT_SERVER_NAME=dns.adguard.com
```

## How It Works

1. **Server Detection**: The forwarder automatically detects which servers are configured for DoT based on the `DOT_SERVERS` list
2. **Health Checks**: DoT servers are included in periodic health checks using TLS connections to ensure they're reachable
3. **TLS Connection**: When querying a DoT server, it establishes a TLS connection on port 853
4. **Certificate Verification**: Validates the server's TLS certificate using the `DOT_SERVER_NAME`
5. **Fallback**: If DoT fails and `DOT_FALLBACK_TO_UDP=true`, automatically retries with UDP
6. **Connection Pooling**: TLS connections are pooled for better performance
7. **Cache Management**: Only reachable DoT servers are kept in the active server cache

## Mixed Configuration

You can use both regular DNS and DoT servers simultaneously:

```bash
# Regular UDP DNS servers
PRIVATE_DNS_SERVERS=192.168.1.1:53
PUBLIC_DNS_SERVERS=1.1.1.1:53

# DoT servers (for extra privacy)
ENABLE_DOT=true
DOT_SERVERS=8.8.8.8:853,9.9.9.9:853
DOT_SERVER_NAME=dns.google
```

The forwarder will use DoT for servers in the `DOT_SERVERS` list and regular UDP for others.

## Performance Considerations

- **Initial Connection**: TLS handshake adds ~50-100ms latency on first query
- **Connection Reuse**: Subsequent queries reuse TLS connections (minimal overhead)
- **CPU Usage**: TLS encryption increases CPU usage slightly
- **Caching**: The forwarder's cache layer minimizes upstream queries, reducing DoT overhead

## Docker Example

```yaml
version: '3.8'
services:
  dnsforwarder:
    image: dnsforwarder:latest
    ports:
      - "53:53/udp"
    environment:
      - ENABLE_DOT=true
      - DOT_SERVERS=1.1.1.1:853,8.8.8.8:853
      - DOT_SERVER_NAME=cloudflare-dns.com
      - DOT_SKIP_VERIFY=false
      - DOT_FALLBACK_TO_UDP=true
      - CACHE_SIZE=10000
      - DNS_CACHE_TTL=30m
    restart: unless-stopped
```

## Monitoring

DoT queries are tracked in Prometheus metrics with protocol labels:
- `upstream_protocol_dot`: Count of DoT queries
- `upstream_protocol_udp`: Count of UDP queries
- `upstream_query_failed`: Failed query count (includes DoT failures)
- `upstream_server_reachable`: Shows which DoT servers passed health checks (1=reachable, 0=unreachable)

DoT servers are health-checked periodically (configurable via `CACHE_SERVERS_REFRESH`). Only servers that pass health checks are used for queries.

## Troubleshooting

### DoT Connection Fails

1. **Check port 853 connectivity**:
   ```bash
   telnet 1.1.1.1 853
   ```

2. **Verify DNS resolution of server name**:
   ```bash
   nslookup cloudflare-dns.com
   ```

3. **Enable debug logging**:
   ```bash
   LOG_LEVEL=debug
   ```

4. **Test with certificate verification disabled** (temporary):
   ```bash
   DOT_SKIP_VERIFY=true
   ```
   If this works, the issue is likely with certificate validation.

### Certificate Verification Errors

- Ensure `DOT_SERVER_NAME` matches the certificate's CN or SAN
- Check that system time is correct (certificate validity)
- Verify CA certificates are up to date in the container

### Performance Issues

- Enable `DOT_FALLBACK_TO_UDP=true` for reliability
- Increase cache size to reduce upstream queries
- Consider using DoT only for sensitive queries via domain routing

## Security Best Practices

1. **Always verify certificates** (`DOT_SKIP_VERIFY=false`)
2. **Use reputable DoT providers** with good uptime
3. **Set proper DOT_SERVER_NAME** for certificate validation
4. **Enable fallback** unless you require DoT-only operation
5. **Monitor metrics** to detect DoT failures
6. **Keep container updated** for latest TLS security patches

## Limitations

- Currently supports DoT only (not DoH or DoQ)
- TLS connection pooling is per-server
- Certificate pinning is not supported
- DNSSEC validation happens at upstream server (not locally)

## Future Enhancements

- DNS-over-HTTPS (DoH) support
- DNS-over-QUIC (DoQ) support
- Per-server protocol configuration
- Certificate pinning options
- DNSSEC validation
