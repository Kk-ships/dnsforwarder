# DNS Forwarder

A high-performance, cache-enabled DNS forwarder written in Go. This project forwards DNS queries to upstream servers, caches responses for improved performance, and provides detailed logging and statistics.

## Features
- **DNS Forwarding:** Forwards DNS queries to one or more upstream DNS servers.
- **Caching:** Uses an in-memory cache to store DNS responses, reducing latency and upstream load.
- **Health Checks:** Periodically checks upstream DNS server reachability and only uses healthy servers.
- **Statistics:** Logs DNS usage and cache hit/miss rates.
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
docker run --rm -p 53:53/udp --env-file .env dnsforwarder
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
```

- **DNS_SERVERS:** Comma-separated list of upstream DNS servers.
- **CACHE_TTL:** How long to cache the list of healthy DNS servers.
- **DNS_TIMEOUT:** Timeout for DNS queries to upstream servers.
- **WORKER_COUNT:** Number of concurrent health checks for upstream servers.
- **DNS_PORT:** Port to listen on (default `:53`).
- **CACHE_SIZE:** Maximum number of DNS entries to cache.
- **DNS_CACHE_TTL:** How long to cache DNS responses.

### 5. Logging & Stats
- Logs are written to stdout and kept in a ring buffer for diagnostics.
- Periodic logs show DNS usage and cache hit/miss rates.

## License
GNU General Public License v3.0

## Credits
- [miekg/dns](https://github.com/miekg/dns)
- [patrickmn/go-cache](https://github.com/patrickmn/go-cache)
