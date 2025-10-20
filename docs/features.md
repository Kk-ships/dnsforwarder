# Features

- **DNS Forwarding:** Forwards DNS queries to one or more upstream DNS servers.
- **Client-Based Routing:** Route different clients to different upstream DNS servers (private/public).
- **Domain Routing (Folder-Based):** Forward DNS queries for specific domains to designated upstream servers using rules from all `.txt` files in a folder.
- **Hot-Reload Routing Table:** Automatically refresh domain routing rules at a configurable interval.
- **Caching:** Uses an in-memory cache to store DNS responses, reducing latency and upstream load.
- **Stale Cache Updater:** Proactively refreshes frequently accessed cache entries before they expire to prevent cache misses for popular domains.
- **Cache Persistence:** Automatically saves and restores cache to/from disk for hot starts after container restarts.
- **Health Checks:** Periodically checks upstream DNS server reachability and only uses healthy servers.
- **Statistics:** Logs DNS usage and cache hit/miss rates.
- **Prometheus Metrics:** Comprehensive metrics collection for monitoring and alerting.
- **Configurable:** All major parameters (DNS servers, cache TTL, ports, metrics, routing folders, reload intervals, etc.) are configurable via environment variables or a `.env` file.
- **Docker Support:** Lightweight, production-ready Docker image with secure, dynamic configuration via folder mounts.
