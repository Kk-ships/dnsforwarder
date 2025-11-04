# Client-Based DNS Routing

The DNS forwarder supports intelligent client-based routing, allowing you to direct different clients to different upstream DNS servers. This is perfect for scenarios where you want:

- **Private DNS servers** (like PiHole or AdGuard Home) for most devices
- **Public DNS servers** (like Cloudflare or Google) as fallback or for specific devices
- **Centralized management** without touching individual device network settings

## How It Works

1. **Default Behavior (`ENABLE_CLIENT_ROUTING=false`):**
   - All clients use the same upstream servers defined in `PRIVATE_DNS_SERVERS` first
     and `PUBLIC_DNS_SERVERS` as fallback.

2. **Client Routing Enabled (`ENABLE_CLIENT_ROUTING=true`):**
   - Most clients use `PRIVATE_DNS_SERVERS` first, with `PUBLIC_DNS_SERVERS` as fallback.
   - Clients listed in `PUBLIC_ONLY_CLIENTS` (by IP) or `PUBLIC_ONLY_CLIENT_MACS` (by MAC address) use only `PUBLIC_DNS_SERVERS`.
   - Health checks ensure only reachable servers are used.

## MAC Address-Based Routing

If your network uses DHCP and client IPs change frequently, you can use MAC addresses to identify clients that should always use public DNS servers (bypassing ad-blocking, etc). This is similar to how Pi-hole can whitelist by MAC address.

Set the `PUBLIC_ONLY_CLIENT_MACS` environment variable to a comma-separated list of MAC addresses (e.g., `00:11:22:33:44:55,AA:BB:CC:DD:EE:FF`). The DNS forwarder will attempt to resolve the MAC address for each client IP using the ARP table. If a match is found, that client will be routed to public DNS servers only, regardless of its current IP address.

**Note:** MAC address detection works only for clients on the same local network segment as the DNS forwarder (LAN). It will not work for remote clients or across routers.

## MAC Vendor Prefix (OUI) Based Routing

For scenarios where you want to route all devices from a specific manufacturer, you can use MAC vendor prefixes (also known as OUI - Organizationally Unique Identifier). This is perfect for IoT devices or specific brands that need special DNS handling.

Set the `PUBLIC_ONLY_CLIENT_MAC_OUIS` environment variable to a comma-separated list of OUI prefixes. An OUI is the first 3 octets (6 hex digits) of a MAC address that identifies the manufacturer.

**Example Use Cases:**
- Route all IoT devices to a restricted DNS server that only allows NTP servers
- Send all Amazon devices (Echo, Fire TV) to public DNS to avoid blocking their services
- Direct all Google devices (Nest, Chromecast) to specific DNS servers

**Common OUI Prefixes:**
- TUYA Smart: `68:57:2d`, `10:5a:17`, `d8:1f:12`
- Amazon/Eero: `f0:d7:aa`, `ac:a6:2f`
- Google Nest: `64:16:66`, `f4:f5:d8`
- TP-Link: `a4:2b:b0`, `50:c7:bf`

You can find OUI prefixes for any manufacturer at [IEEE OUI Database](https://standards-oui.ieee.org/) or [MAC Vendors Database](https://macvendors.com/).

**Example Configuration:**
```bash
# Route all TUYA devices to a restricted DNS (NTP-only)
PUBLIC_ONLY_CLIENT_MAC_OUIS=68:57:2d,10:5a:17,d8:1f:12
PUBLIC_DNS_SERVERS=192.168.1.50:53  # Your NTP-only DNS server
```

**Note:** Like MAC address routing, OUI matching only works for clients on the same local network segment (LAN).

## Example `.env` Configuration

```
ENABLE_CLIENT_ROUTING=true
PUBLIC_ONLY_CLIENTS=192.168.1.100,10.0.0.50
PUBLIC_ONLY_CLIENT_MACS=00:11:22:33:44:55,AA:BB:CC:DD:EE:FF
PUBLIC_ONLY_CLIENT_MAC_OUIS=68:57:2d,10:5a:17
```

## Example Scenarios

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
PUBLIC_ONLY_CLIENT_MAC_OUIS=68:57:2d                  # All TUYA devices (by OUI)
```

**Scenario 3: Corporate Environment**
```bash
ENABLE_CLIENT_ROUTING=true
PRIVATE_DNS_SERVERS=10.10.1.1:53,10.10.1.2:53       # Internal DNS
PUBLIC_DNS_SERVERS=8.8.8.8:53,1.1.1.1:53
PUBLIC_ONLY_CLIENTS=10.10.2.100,10.10.2.101         # Guest network devices (by IP)
PUBLIC_ONLY_CLIENT_MACS=11:22:33:44:55:66           # Guest device (by MAC)
```

**Scenario 4: IoT Device Control (TUYA devices to restricted DNS)**
```bash
ENABLE_CLIENT_ROUTING=true
PRIVATE_DNS_SERVERS=192.168.1.10:53                 # Your PiHole for normal devices
PUBLIC_DNS_SERVERS=192.168.1.50:53                  # Restricted DNS (NTP-only, no external access)
PUBLIC_ONLY_CLIENT_MAC_OUIS=68:57:2d,10:5a:17,d8:1f:12  # TUYA device OUIs
```
In this scenario, all TUYA IoT devices will use the restricted DNS server at `192.168.1.50:53` which could be configured to only allow NTP servers and block all other internet access, effectively sandboxing your IoT devices.

## Benefits
- **Centralized Control:** Change DNS behavior without touching individual devices
- **Flexibility:** Easy to test different configurations or troubleshoot problematic devices
- **Reliability:** Automatic fallback ensures DNS always works
- **Performance:** Private servers first, public as backup
- **Monitoring:** All DNS routing decisions are logged and can be monitored via Prometheus metrics
