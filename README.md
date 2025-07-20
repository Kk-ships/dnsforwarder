# DNS Forwarder

A high-performance, cache-enabled DNS forwarder written in Go. This project forwards DNS queries to upstream servers, caches responses for improved performance, and provides detailed logging and statistics.

## Features
- **DNS Forwarding:** Forwards DNS queries to one or more upstream DNS servers.
- **Client-Based Routing:** Route different clients to different upstream DNS servers (private/public).
- **Caching:** Uses an in-memory cache to store DNS responses, reducing latency and upstream load.
- **Health Checks:** Periodically checks upstream DNS server reachability and only uses healthy servers.
- **Statistics:** Logs DNS usage and cache hit/miss rates.
- **Prometheus Metrics:** Comprehensive metrics collection for monitoring and alerting.
- **Configurable:** All major parameters (DNS servers, cache TTL, ports, etc.) are configurable via environment variables or a `.env` file.
- **Docker Support:** Lightweight, production-ready Docker image.

## Usage

### 1. Build and Run Locally

#### Prerequisites
- Go 1.24+
- [miekg/dns](https://github.com/miekg/dns) and [patrickmn/go-cache](https://github.com/patrickmn/go-cache) (installed via `go mod`)

#### Build
```sh
go build -o dnsforwarder .
```

#### Run
```sh
./dnsforwarder
```

### 2. Run with Docker

#### Build the Docker image
```sh
docker build -t dnsforwarder .
```

#### Run the container
```sh
docker run --rm -p 53:53/udp -p 8080:8080 --env-file .env dnsforwarder
```


### 3. Run with Docker Compose

You can also use Docker Compose for easy deployment.

Start with:
```sh
docker compose up --build
```

---

### 4. Configuration

You can configure the forwarder using environment variables or a `.env` file. Example `.env`:

```
DNS_SERVERS=192.168.0.110:53,8.8.8.8:53
CACHE_TTL=10s
DNS_TIMEOUT=5s
WORKER_COUNT=5
TEST_DOMAIN=google.com
DNS_PORT=:53
UDP_SIZE=65535
DNS_STATSLOG=1m
DEFAULT_DNS_SERVER=8.8.8.8:53
CACHE_SIZE=10000
DNS_CACHE_TTL=30m
ENABLE_METRICS=true
METRICS_PORT=:8080
METRICS_PATH=/metrics
METRICS_UPDATE_INTERVAL=30s

# Client-Based DNS Routing (Optional)
ENABLE_CLIENT_ROUTING=true
PRIVATE_DNS_SERVERS=192.168.1.1:53,192.168.1.2:53
PUBLIC_DNS_SERVERS=1.1.1.1:53,8.8.8.8:53
PUBLIC_ONLY_CLIENTS=192.168.1.100,10.0.0.50
```

#### Basic Configuration
- **DNS_SERVERS:** Comma-separated list of upstream DNS servers.
- **CACHE_TTL:** How long to cache the list of healthy DNS servers.
- **DNS_TIMEOUT:** Timeout for DNS queries to upstream servers.
- **WORKER_COUNT:** Number of concurrent health checks for upstream servers.
- **DNS_PORT:** Port to listen on (default `:53`).
- **CACHE_SIZE:** Maximum number of DNS entries to cache.
- **DNS_CACHE_TTL:** How long to cache DNS responses.
- **ENABLE_METRICS:** Enable Prometheus metrics (default `true`).
- **METRICS_PORT:** Port for Prometheus metrics endpoint (default `:8080`).
- **METRICS_PATH:** Path for metrics endpoint (default `/metrics`).
- **METRICS_UPDATE_INTERVAL:** How often to update system metrics (default `30s`).

#### Client-Based DNS Routing Configuration
- **ENABLE_CLIENT_ROUTING:** Enable client-based DNS routing (default `false`).
- **PRIVATE_DNS_SERVERS:** Comma-separated list of private DNS servers (e.g., PiHole, AdGuard Home).
- **PUBLIC_DNS_SERVERS:** Comma-separated list of public DNS servers (e.g., Cloudflare, Google).
- **PUBLIC_ONLY_CLIENTS:** Comma-separated list of client IPs that should only use public DNS servers.

### 5. Client-Based DNS Routing

The DNS forwarder supports intelligent client-based routing, allowing you to direct different clients to different upstream DNS servers. This is perfect for scenarios where you want:

- **Private DNS servers** (like PiHole or AdGuard Home) for most devices
- **Public DNS servers** (like Cloudflare or Google) as fallback or for specific devices
- **Centralized management** without touching individual device network settings

#### How It Works

1. **Default Behavior (`ENABLE_CLIENT_ROUTING=false`):**
   - All clients use the same upstream servers defined in `DNS_SERVERS`.

2. **Client Routing Enabled (`ENABLE_CLIENT_ROUTING=true`):**
   - Most clients use `PRIVATE_DNS_SERVERS` first, with `PUBLIC_DNS_SERVERS` as fallback.
   - Clients listed in `PUBLIC_ONLY_CLIENTS` (by IP) or `PUBLIC_ONLY_CLIENT_MACS` (by MAC address) use only `PUBLIC_DNS_SERVERS`.
   - Health checks ensure only reachable servers are used.

### MAC Address-Based Routing

If your network uses DHCP and client IPs change frequently, you can use MAC addresses to identify clients that should always use public DNS servers (bypassing ad-blocking, etc). This is similar to how Pi-hole can whitelist by MAC address.

Set the `PUBLIC_ONLY_CLIENT_MACS` environment variable to a comma-separated list of MAC addresses (e.g., `00:11:22:33:44:55,AA:BB:CC:DD:EE:FF`). The DNS forwarder will attempt to resolve the MAC address for each client IP using the ARP table. If a match is found, that client will be routed to public DNS servers only, regardless of its current IP address.

**Note:** MAC address detection works only for clients on the same local network segment as the DNS forwarder (LAN). It will not work for remote clients or across routers.

#### Example `.env` Configuration

```
ENABLE_CLIENT_ROUTING=true
PRIVATE_DNS_SERVERS=192.168.1.10:53,192.168.1.11:53
PUBLIC_DNS_SERVERS=1.1.1.1:53,8.8.8.8:53
PUBLIC_ONLY_CLIENTS=192.168.1.100,10.0.0.50
PUBLIC_ONLY_CLIENT_MACS=00:11:22:33:44:55,AA:BB:CC:DD:EE:FF
```

#### Example Scenarios

**Scenario 1: PiHole with Public Fallback**
```bash
ENABLE_CLIENT_ROUTING=true
PRIVATE_DNS_SERVERS=192.168.1.10:53    # Your PiHole
PUBLIC_DNS_SERVERS=1.1.1.1:53,8.8.8.8:53
PUBLIC_ONLY_CLIENTS=192.168.1.100      # Problematic device (by IP)
PUBLIC_ONLY_CLIENT_MACS=00:11:22:33:44:55 # Problematic device (by MAC)
```

**Scenario 2: Multiple Private Servers**
```bash
ENABLE_CLIENT_ROUTING=true
PRIVATE_DNS_SERVERS=192.168.1.10:53,192.168.1.11:53  # PiHole + AdGuard
PUBLIC_DNS_SERVERS=1.1.1.1:53,9.9.9.9:53
PUBLIC_ONLY_CLIENTS=192.168.1.100,10.0.0.50          # Testing devices (by IP)
PUBLIC_ONLY_CLIENT_MACS=AA:BB:CC:DD:EE:FF             # Testing device (by MAC)
```

**Scenario 3: Corporate Environment**
```bash
ENABLE_CLIENT_ROUTING=true
PRIVATE_DNS_SERVERS=10.10.1.1:53,10.10.1.2:53       # Internal DNS
PUBLIC_DNS_SERVERS=8.8.8.8:53,1.1.1.1:53
PUBLIC_ONLY_CLIENTS=10.10.2.100,10.10.2.101         # Guest network devices (by IP)
PUBLIC_ONLY_CLIENT_MACS=11:22:33:44:55:66           # Guest device (by MAC)
```
#### Benefits
- **Centralized Control:** Change DNS behavior without touching individual devices
- **Flexibility:** Easy to test different configurations or troubleshoot problematic devices
- **Reliability:** Automatic fallback ensures DNS always works
- **Performance:** Private servers first, public as backup
- **Monitoring:** All DNS routing decisions are logged and can be monitored via Prometheus metrics

### 6. Logging & Stats
- Logs are written to stdout and kept in a ring buffer for diagnostics.
- Periodic logs show DNS usage and cache hit/miss rates.
- When client routing is enabled, logs show which servers are being used for each client.
- Client routing decisions and server health status are logged for troubleshooting.

### 7. Prometheus Metrics
When `ENABLE_METRICS=true`, the following metrics are available at `/metrics` endpoint:

#### DNS Query Metrics
- `dns_queries_total` - Total DNS queries processed (by type and status)
- `dns_query_duration_seconds` - DNS query duration histogram

#### Cache Metrics
- `dns_cache_hits_total` - Total cache hits
- `dns_cache_misses_total` - Total cache misses
- `dns_cache_size` - Current cache size

#### Upstream Server Metrics
- `dns_upstream_queries_total` - Queries sent to upstream servers
- `dns_upstream_query_duration_seconds` - Upstream query duration
- `dns_upstream_servers_reachable` - Server reachability status
- `dns_upstream_servers_total` - Total configured servers

#### System Metrics
- `dns_server_uptime_seconds_total` - Server uptime
- `dns_server_memory_usage_bytes` - Memory usage
- `dns_server_goroutines` - Active goroutines

#### Health Check Endpoints
- `/health` - Simple health check (returns "OK")
- `/status` - JSON status response

#### Example Prometheus Configuration
```yaml
scrape_configs:
  - job_name: 'dns-forwarder'
    static_configs:
      - targets: ['localhost:8080']
    metrics_path: '/metrics'
    scrape_interval: 30s
```

## License
GNU General Public License v3.0

## Credits
- [miekg/dns](https://github.com/miekg/dns)
- [patrickmn/go-cache](https://github.com/patrickmn/go-cache)
