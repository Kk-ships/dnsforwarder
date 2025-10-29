# What's New

- **DNS-over-TLS (DoT) Support:** Encrypt your upstream DNS queries using TLS on port 853 for enhanced privacy and security. Supports automatic fallback to UDP and popular providers like Cloudflare, Google, and Quad9.
- **PID File Management:** Integrated PID file support for monitoring tools like monit, Zabbix, and systemd.
- **Stale Cache Updater:** Automatically refresh frequently accessed cache entries before they expire, ensuring popular domains always have fresh responses without client-facing delays.
- **Cache Persistence (Hot Start):** DNS cache is now persisted to disk and automatically restored on container restarts, providing faster response times after restarts. Cache for hot restart is valid for 1 hour only.
- **Folder-based Domain Routing:** Load domain routing rules from all `.txt` files in a specified folder, making management and updates easier.
- **Hot-Reload Domain Routing Table:** Automatically refresh domain routing rules at a configurable interval without restarting the service.
- **Enhanced Configuration:** All major parameters (DNS servers, cache TTL, ports, metrics, routing folders, reload intervals, etc.) are configurable via environment variables or a `.env` file.
- **Improved Docker Support:** Easily mount domain routing folders as read-only volumes for secure and dynamic configuration in containers.
