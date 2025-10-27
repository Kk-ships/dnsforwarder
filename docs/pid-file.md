# DNS Forwarder PID File Configuration Examples

## Quick Decision Guide

| Deployment Scenario | PID File Needed? | Configuration |
|---------------------|------------------|---------------|
| Docker container | ❌ No (recommended) | `PID_FILE=""` or omit |
| Docker with host monitoring | ✅ Yes | Mount volume + `PID_FILE=/var/run/dnsforwarder/dnsforwarder.pid` |
| Kubernetes/Orchestrator | ❌ No | Use native probes |
| Systemd service | ✅ Yes | `PID_FILE=/var/run/dnsforwarder.pid` |
| Direct binary execution | ✅ Yes (if monitoring) | `PID_FILE=/var/run/dnsforwarder.pid` |
| Monit/Zabbix on host | ✅ Yes | `PID_FILE=/var/run/dnsforwarder.pid` |

## Environment Variable Configuration

The DNS forwarder now supports PID file creation for monitoring tools like monit, Zabbix, etc.

### Basic Usage

Set the `PID_FILE` environment variable to specify where the PID file should be created:

```bash
export PID_FILE="/var/run/dnsforwarder.pid"
./dnsforwarder
```

### Default Value

If `PID_FILE` is not set, it defaults to `/var/run/dnsforwarder.pid`.

### Disabling PID File

To disable PID file creation entirely, set the environment variable to an empty string:

```bash
export PID_FILE=""
./dnsforwarder
```

## Monitoring Tool Integration

### Monit Configuration Example

```monit
check process dnsforwarder with pidfile /var/run/dnsforwarder.pid
  start program = "/usr/local/bin/dnsforwarder"
  stop program = "/bin/kill -TERM `/bin/cat /var/run/dnsforwarder.pid`"
  if failed host 127.0.0.1 port 53 protocol dns then restart
  if cpu usage > 80% for 2 cycles then alert
  if memory usage > 80% for 2 cycles then alert
  if 3 restarts within 5 cycles then timeout
```

### Systemd Service Example

```ini
[Unit]
Description=DNS Forwarder Service
After=network.target

[Service]
Type=forking
PIDFile=/var/run/dnsforwarder.pid
Environment=PID_FILE=/var/run/dnsforwarder.pid
Environment=DNS_PORT=:53
Environment=LOG_LEVEL=info
ExecStart=/usr/local/bin/dnsforwarder
ExecReload=/bin/kill -HUP $MAINPID
KillMode=process
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

### Zabbix Monitoring Example

Create a Zabbix user parameter to monitor the DNS forwarder process:

```
# /etc/zabbix/zabbix_agentd.d/dnsforwarder.conf
UserParameter=dnsforwarder.status,if [ -f /var/run/dnsforwarder.pid ] && kill -0 `cat /var/run/dnsforwarder.pid` 2>/dev/null; then echo 1; else echo 0; fi
UserParameter=dnsforwarder.pid,cat /var/run/dnsforwarder.pid 2>/dev/null || echo 0
UserParameter=dnsforwarder.uptime,if [ -f /var/run/dnsforwarder.pid ]; then ps -o etime= -p `cat /var/run/dnsforwarder.pid` 2>/dev/null | tr -d ' '; else echo 0; fi
```

### Docker Configuration

#### Understanding PID Files in Docker

When running in Docker containers, PID file behavior depends on your monitoring approach:

**1. Monitoring the Container (Recommended)**

Docker and orchestration tools like Docker Compose, Kubernetes, and systemd already track container processes. For this use case, you typically **don't need a PID file** inside the container:

```yaml
version: '3.8'
services:
  dnsforwarder:
    image: dnsforwarder:latest
    ports:
      - "53:53/udp"
    environment:
      - PID_FILE=  # Disable PID file (empty string)
      - LOG_LEVEL=info
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "nc", "-zu", "localhost", "53"]
      interval: 30s
      timeout: 5s
      retries: 3
```

Use Docker's native health checks and monitoring:
```bash
# Check if container is running
docker ps | grep dnsforwarder

# Check container health
docker inspect --format='{{.State.Health.Status}}' dnsforwarder

# Monitor with docker stats
docker stats dnsforwarder
```

**2. Monitoring from the Host System**

If you need to monitor the process **from the host** (e.g., with monit or Zabbix running on the host), you must:
- Mount a volume to expose the PID file to the host
- Ensure the PID file path is writable

```bash
docker run -d \
  --name dnsforwarder \
  -p 53:53/udp \
  -v /var/run/dnsforwarder:/var/run/dnsforwarder \
  -e PID_FILE=/var/run/dnsforwarder/dnsforwarder.pid \
  dnsforwarder:latest
```

Or with docker-compose:

```yaml
version: '3.8'
services:
  dnsforwarder:
    image: dnsforwarder:latest
    ports:
      - "53:53/udp"
    volumes:
      - /var/run/dnsforwarder:/var/run/dnsforwarder
    environment:
      - PID_FILE=/var/run/dnsforwarder/dnsforwarder.pid
      - LOG_LEVEL=info
    restart: unless-stopped
```

Then monitor from the host:

```bash
# Host-side monit configuration
check process dnsforwarder with pidfile /var/run/dnsforwarder/dnsforwarder.pid
  if failed host 127.0.0.1 port 53 protocol dns then alert
```

**3. Important Notes for Docker:**

- **PID Namespace:** PIDs inside containers are isolated. The PID written to the file is the process ID **within the container's namespace**, not the host PID.
- **Container Restart:** When a container restarts, it gets a new PID. The PID file is automatically cleaned up and recreated.
- **Scratch Base Image:** This project uses a `FROM scratch` base image, which has no shell or tools. The PID file is written directly by the Go application.
- **Permissions:** Ensure the mounted directory is writable by the container user (default: root in this image).

**4. Kubernetes/Orchestration**

For Kubernetes or other orchestrators, use their native monitoring:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: dnsforwarder
spec:
  containers:
  - name: dnsforwarder
    image: dnsforwarder:latest
    env:
    - name: PID_FILE
      value: ""  # Disable PID file
    livenessProbe:
      tcpSocket:
        port: 53
      initialDelaySeconds: 15
      periodSeconds: 20
    readinessProbe:
      tcpSocket:
        port: 53
      initialDelaySeconds: 5
      periodSeconds: 10
```

## Behavior

- **Startup**: The DNS forwarder will create the PID file before starting the DNS server
- **Running Process Detection**: If a PID file exists with a running process, startup will fail with an error
- **Stale File Cleanup**: If a PID file exists but the process is not running, it will be automatically removed
- **Graceful Shutdown**: The PID file is removed during graceful shutdown (SIGTERM, SIGINT)
- **Directory Creation**: The directory structure for the PID file will be created automatically if it doesn't exist

## Permissions

Ensure the user running the DNS forwarder has write permissions to the PID file directory. For system-wide installations:

```bash
# Create the directory with appropriate permissions
sudo mkdir -p /var/run
sudo chown dnsforwarder:dnsforwarder /var/run

# Or use a user-specific directory
mkdir -p ~/.local/var/run
export PID_FILE="$HOME/.local/var/run/dnsforwarder.pid"
```

## Common Scenarios Explained

### Scenario 1: Running Natively with Monit (PID file needed)

```bash
# Start the DNS forwarder
export PID_FILE=/var/run/dnsforwarder.pid
./dnsforwarder
```

Monit configuration:
```monit
check process dnsforwarder with pidfile /var/run/dnsforwarder.pid
  start program = "/usr/local/bin/dnsforwarder"
  stop program = "/bin/kill -TERM `/bin/cat /var/run/dnsforwarder.pid`"
  if failed host 127.0.0.1 port 53 protocol dns then restart
```

### Scenario 2: Docker Container (PID file NOT needed)

```bash
# Docker manages the process lifecycle
docker run -d \
  --name dnsforwarder \
  -p 53:53/udp \
  -e PID_FILE="" \
  --restart unless-stopped \
  dnsforwarder:latest

# Monitor with Docker
docker ps
docker logs dnsforwarder
docker stats dnsforwarder
```

### Scenario 3: Docker + Host Monitoring (PID file needed)

If you want monit/Zabbix on the **host** to monitor a **containerized** process:

```bash
# Create directory on host
sudo mkdir -p /var/run/dnsforwarder

# Run container with mounted PID file location
docker run -d \
  --name dnsforwarder \
  -p 53:53/udp \
  -v /var/run/dnsforwarder:/var/run/dnsforwarder \
  -e PID_FILE=/var/run/dnsforwarder/dnsforwarder.pid \
  dnsforwarder:latest

# Now monit on host can read the PID file
# Note: The PID in the file is the container's internal PID, not the host PID
```

**Important:** The PID in the file will be the process ID **inside the container's namespace** (usually PID 1), which is different from the host's perspective. This approach has limitations:

- The PID file shows the container-internal PID (not useful for host-level process monitoring)
- Better to monitor the container itself: `docker inspect --format='{{.State.Running}}' dnsforwarder`
- Or use Docker health checks

### Scenario 4: Systemd Service (PID file optional but recommended)

```ini
[Unit]
Description=DNS Forwarder Service
After=network.target

[Service]
Type=simple
Environment=PID_FILE=/var/run/dnsforwarder.pid
ExecStart=/usr/local/bin/dnsforwarder
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

**Note:** For `Type=simple`, systemd tracks the process itself, so the PID file is optional but can be useful for external monitoring tools.
