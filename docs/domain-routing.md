# Domain Routing

Domain routing allows you to forward DNS queries for specific domains to specific upstream DNS servers. This is useful for scenarios such as:
- Sending queries for internal domains to a private DNS server
- Forwarding queries for certain public domains to a specific provider
- Overriding DNS for selected domains

## How It Works
- When `ENABLE_DOMAIN_ROUTING=true` is set, the DNS forwarder loads domain routing rules from all `.txt` files in the folder specified by the `DOMAIN_ROUTING_FOLDER` environment variable.
- Each file should contain rules in the format: `/domain/ip`, one per line. Lines starting with `#` are treated as comments.
- When a DNS query matches a domain in the routing table, the query is forwarded to the specified IP address for that domain.
- If no match is found, normal client or default routing is used.
- The routing table is automatically refreshed at intervals specified by `DOMAIN_ROUTING_TABLE_RELOAD_INTERVAL` in seconds (default: 60 seconds).

## Example Configuration
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

## Notes
- The domain routing file must be a plain text file, not a directory.
- Routing file follows dnsmasq conventions of domain routing for easier integration with existing tools.
- Each rule must have both a domain and an IP address, separated by `/`.
- The routing table is loaded at startup and logged for diagnostics.
- If no domain routing files are specified, domain routing will not be enabled.
- If a domain is not found in the routing table, the forwarder falls back to client or default routing.

## Troubleshooting
- Check logs for messages about domain routing initialization and file loading errors.
- Make sure the file paths in `DOMAIN_ROUTING_FOLDER` are correct and accessible by the DNS forwarder.
- Ensure each rule is in the correct format and not commented out.

## Example: Docker Compose with Domain Routing

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
