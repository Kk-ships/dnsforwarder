
# DNS Forwarder

A high-performance, cache-enabled DNS forwarder written in Go. This project forwards DNS queries to upstream servers, caches responses for improved performance, and provides detailed logging, statistics, and flexible routing features.

## What's New
- **Cache Persistence (Hot Start):** DNS cache is now persisted to disk and automatically restored on container restarts, providing faster response times after restarts. Cache for hot restart is valid for 1 hour only.
- **Folder-based Domain Routing:** Load domain routing rules from all `.txt` files in a specified folder, making management and updates easier.
- **Hot-Reload Domain Routing Table:** Automatically refresh domain routing rules at a configurable interval without restarting the service.
- **Enhanced Configuration:** All major parameters (DNS servers, cache TTL, ports, metrics, routing folders, reload intervals, etc.) are configurable via environment variables or a `.env` file.
- **Improved Docker Support:** Easily mount domain routing folders as read-only volumes for secure and dynamic configuration in containers.


## Features
- **DNS Forwarding:** Forwards DNS queries to one or more upstream DNS servers.
- **Client-Based Routing:** Route different clients to different upstream DNS servers (private/public).
- **Domain Routing (Folder-Based):** Forward DNS queries for specific domains to designated upstream servers using rules from all `.txt` files in a folder.
- **Hot-Reload Routing Table:** Automatically refresh domain routing rules at a configurable interval.
- **Caching:** Uses an in-memory cache to store DNS responses, reducing latency and upstream load.
- **Cache Persistence:** Automatically saves and restores cache to/from disk for hot starts after container restarts.
- **Health Checks:** Periodically checks upstream DNS server reachability and only uses healthy servers.
- **Statistics:** Logs DNS usage and cache hit/miss rates.
- **Prometheus Metrics:** Comprehensive metrics collection for monitoring and alerting.
- **Configurable:** All major parameters (DNS servers, cache TTL, ports, metrics, routing folders, reload intervals, etc.) are configurable via environment variables or a `.env` file.
- **Docker Support:** Lightweight, production-ready Docker image with secure, dynamic configuration via folder mounts.

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

#### Basic Configuration
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


#### Metric Configuration
- **ENABLE_METRICS:** Enable Prometheus metrics (default `true`).
- **METRICS_PORT:** Port for Prometheus metrics endpoint (default `:8080`).
- **METRICS_PATH:** Path for metrics endpoint (default `/metrics`).
- **METRICS_UPDATE_INTERVAL:** How often to update system metrics (default `30s`).

#### Client-Based DNS Routing Configuration
- **ENABLE_CLIENT_ROUTING:** Enable client-based DNS routing (default `false`).
- **PUBLIC_ONLY_CLIENTS:** Comma-separated list of client IPs that should only use public DNS servers.
- **PUBLIC_ONLY_CLIENTS_MAC** Comma-separated list of client MAC addresses that should only use public DNS servers.

#### Domain Routing Configuration
- **ENABLE_DOMAIN_ROUTING:** Enable domain routing (default `false`).
- **DOMAIN_ROUTING_FOLDER:** Comma-separated list of folders containing routing configuration files
- **DOMAIN_ROUTING_TABLE_RELOAD_INTERVAL** Domain routing reload interval in seconds

#### Cache Persistence Configuration
- **ENABLE_CACHE_PERSISTENCE:** Enable cache persistence to disk for hot starts (default `true`).
- **CACHE_PERSISTENCE_FILE:** Path to the cache persistence file (default `/app/cache/dns_cache.json`).
- **CACHE_PERSISTENCE_INTERVAL:** How often to save cache to disk (default `5m`).
- **CACHE_PERSISTENCE_MAX_AGE:** Maximum age of cache file before it's considered stale and ignored (default `1h`).

### 5. Client-Based DNS Routing

The DNS forwarder supports intelligent client-based routing, allowing you to direct different clients to different upstream DNS servers. This is perfect for scenarios where you want:

- **Private DNS servers** (like PiHole or AdGuard Home) for most devices
- **Public DNS servers** (like Cloudflare or Google) as fallback or for specific devices
- **Centralized management** without touching individual device network settings

#### How It Works

1. **Default Behavior (`ENABLE_CLIENT_ROUTING=false`):**
   - All clients use the same upstream servers defined in `PRIVATE_DNS_SERVERS` first
     and `PUBLIC_DNS_SERVERS` as fallback.

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

## Domain Routing

Domain routing allows you to forward DNS queries for specific domains to specific upstream DNS servers. This is useful for scenarios such as:
- Sending queries for internal domains to a private DNS server
- Forwarding queries for certain public domains to a specific provider
- Overriding DNS for selected domains

### How It Works
- When `ENABLE_DOMAIN_ROUTING=true` is set, the DNS forwarder loads domain routing rules from all `.txt` files in the folder specified by the `DOMAIN_ROUTING_FOLDER` environment variable.
- Each file should contain rules in the format: `/domain/ip`, one per line. Lines starting with `#` are treated as comments.
- When a DNS query matches a domain in the routing table, the query is forwarded to the specified IP address for that domain.
- If no match is found, normal client or default routing is used.
- The routing table is automatically refreshed at intervals specified by `DOMAIN_ROUTING_TABLE_RELOAD_INTERVAL` in seconds (default: 60 seconds).

### Example Configuration
Add to your `.env` file:
```
ENABLE_DOMAIN_ROUTING=true
DOMAIN_ROUTING_FOLDER=/etc/dnsforwarder/domain-routes
DOMAIN_ROUTING_TABLE_RELOAD_INTERVAL=60
```

Example `domain-routes.txt`:
```
# Format: /domain/ip
address=/example.com/192.168.1.10
address=/internal.corp/10.10.1.1
# You can add as many rules as needed
```

### Notes
- The domain routing file must be a plain text file, not a directory.
- Routing file follows dnsmasq conventions of domain routing for easier integration with existing tools.
- Each rule must have both a domain and an IP address, separated by `/`.
- The routing table is loaded at startup and logged for diagnostics.
- If no domain routing files are specified, domain routing will not be enabled.
- If a domain is not found in the routing table, the forwarder falls back to client or default routing.

### Troubleshooting
- Check logs for messages about domain routing initialization and file loading errors.
- Make sure the file paths in `DOMAIN_ROUTING_FOLDER` are correct and accessible by the DNS forwarder.
- Ensure each rule is in the correct format and not commented out.

#### Cache Persistence Issues
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

---

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
- [sirupsen/logrus](https://github.com/sirupsen/logrus)
- [prometheus/client_golang](https://github.com/prometheus/client_golang)
- [beorn7/perks](https://github.com/beorn7/perks)
- [cespare/xxhash](https://github.com/cespare/xxhash)
- [klauspost/compress](https://github.com/klauspost/compress)
- [munnerz/goautoneg](https://github.com/munnerz/goautoneg)
- [prometheus/client_model](https://github.com/prometheus/client_model)
- [prometheus/common](https://github.com/prometheus/common)
- [prometheus/procfs](https://github.com/prometheus/procfs)
- [golang.org/x/mod](https://pkg.go.dev/golang.org/x/mod)
- [golang.org/x/net](https://pkg.go.dev/golang.org/x/net)
- [golang.org/x/sync](https://pkg.go.dev/golang.org/x/sync)
- [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys)
- [golang.org/x/tools](https://pkg.go.dev/golang.org/x/tools)
- [google.golang.org/protobuf](https://github.com/protocolbuffers/protobuf-go)


### Example: Docker Compose with Domain Routing

Here is a sample `docker-compose.yml` for running DNS Forwarder with domain routing support:

```yaml
services:
  app:
    build:
      context: .
      args:
        TARGETOS: linux
        TARGETARCH: amd64
    ports:
      - "53:53/udp"
      - "8080:8080" # Optional: Expose metrics on port 8080
    env_file:
      - .env
    restart: always
    environment:
      - TZ=Asia/Kolkata
    volumes:
      - ./domain-routes/:/etc/dnsforwarder/domain-routes/:ro # Mount folder as read-only (recommended)
      # Add more folders/files as needed
```

- Place your domain routing `.txt` files in the `domain-routes/` folder in the project root.
- The folder will be available inside the container at `/etc/dnsforwarder/domain-routes/` (read-only).
- Update your `.env` file to reference the correct path for `DOMAIN_ROUTING_FOLDER`.

**Note:** Mounting the domain routing folder as read-only (`:ro`) is recommended for security and stability.

This setup ensures your custom domain routing configuration is available to the DNS forwarder running in Docker.
