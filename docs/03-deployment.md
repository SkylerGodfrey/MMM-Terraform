# Deployment Guide

This guide covers various deployment scenarios for the Magic Mirror Terraform Agent and Provider.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Agent Deployment](#agent-deployment)
  - [Standard Deployment (systemd)](#standard-deployment-systemd)
  - [Docker Deployment](#docker-deployment)
  - [PM2 Deployment](#pm2-deployment)
- [Provider Installation](#provider-installation)
  - [Local Development](#local-development)
  - [Terraform Registry](#terraform-registry)
- [Network Configuration](#network-configuration)
  - [Local-Only Access](#local-only-access)
  - [LAN Access](#lan-access)
  - [Reverse Proxy with TLS](#reverse-proxy-with-tls)
- [Magic Mirror Setup Variations](#magic-mirror-setup-variations)
  - [PM2 (Default)](#pm2-default)
  - [Systemd Service](#systemd-service)
  - [Docker Magic Mirror](#docker-magic-mirror)
- [High Availability](#high-availability)
- [Backup and Recovery](#backup-and-recovery)
- [Updating](#updating)

---

## Prerequisites

### On Your Development Machine

- Go 1.21 or later
- Terraform 1.0 or later
- SSH access to your Magic Mirror device
- `make` (for using the Makefile)

### On the Magic Mirror Device

- Magic Mirror installed and running
- SSH server enabled
- `sudo` access (for installation)

Check your Raspberry Pi architecture:

```bash
ssh pi@raspberrypi.local "uname -m"
# aarch64 = ARM64 (use build-agent-arm64)
# armv7l  = ARM 32-bit (use build-agent-arm)
```

---

## Agent Deployment

### Standard Deployment (systemd)

This is the recommended approach for most users.

#### Step 1: Build the Agent

On your development machine:

```bash
# For Raspberry Pi 4 / Pi 400 / Pi 5 (64-bit OS)
make build-agent-arm64

# For Raspberry Pi 3 or 32-bit OS
make build-agent-arm
```

#### Step 2: Generate an API Key

```bash
make gen-api-key
# Save the output - you'll need it for configuration
```

#### Step 3: Copy Files to Device

```bash
export MM_HOST=192.168.1.50
export MM_USER=pi

# Copy the binary
scp magicmirror-agent/magicmirror-agent-linux-arm64 ${MM_USER}@${MM_HOST}:/tmp/magicmirror-agent

# Copy the config template
scp magicmirror-agent/config.example.yaml ${MM_USER}@${MM_HOST}:/tmp/config.yaml

# Copy the systemd service
scp deploy/magicmirror-agent.service ${MM_USER}@${MM_HOST}:/tmp/
```

#### Step 4: Install on Device

SSH into the device:

```bash
ssh pi@192.168.1.50
```

Install the binary:

```bash
sudo mv /tmp/magicmirror-agent /usr/local/bin/
sudo chmod +x /usr/local/bin/magicmirror-agent
```

Create and configure the config directory:

```bash
sudo mkdir -p /etc/magicmirror-agent
sudo mv /tmp/config.yaml /etc/magicmirror-agent/config.yaml
sudo nano /etc/magicmirror-agent/config.yaml
```

Update the configuration:

```yaml
server:
  host: "0.0.0.0"    # Listen on all interfaces for remote access
  port: 8484

magicmirror:
  config_path: "/home/pi/MagicMirror/config/config.js"
  restart_command: "pm2 restart MagicMirror"

auth:
  enabled: true
  api_key: "paste-your-generated-api-key-here"
```

Install the systemd service:

```bash
sudo mv /tmp/magicmirror-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable magicmirror-agent
sudo systemctl start magicmirror-agent
```

#### Step 5: Verify

```bash
# Check service status
sudo systemctl status magicmirror-agent

# Check logs
sudo journalctl -u magicmirror-agent -f

# Test the API
curl http://localhost:8484/health
```

---

### Docker Deployment

If you prefer running the agent in Docker.

#### Dockerfile

Create `magicmirror-agent/Dockerfile`:

```dockerfile
FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o magicmirror-agent

FROM alpine:3.19

RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY --from=builder /app/magicmirror-agent .

EXPOSE 8484

ENTRYPOINT ["./magicmirror-agent"]
CMD ["--config", "/etc/magicmirror-agent/config.yaml"]
```

#### Build and Run

```bash
# Build on the Raspberry Pi (or use buildx for cross-compilation)
cd magicmirror-agent
docker build -t magicmirror-agent:latest .

# Run the container
docker run -d \
  --name magicmirror-agent \
  --restart unless-stopped \
  -p 8484:8484 \
  -v /home/pi/MagicMirror/config:/mm-config \
  -v /etc/magicmirror-agent:/etc/magicmirror-agent:ro \
  -v /var/run/docker.sock:/var/run/docker.sock \
  magicmirror-agent:latest
```

#### Docker Compose

Create `docker-compose.yml`:

```yaml
version: '3.8'

services:
  magicmirror-agent:
    build: ./magicmirror-agent
    container_name: magicmirror-agent
    restart: unless-stopped
    ports:
      - "8484:8484"
    volumes:
      - /home/pi/MagicMirror/config:/mm-config
      - ./agent-config.yaml:/etc/magicmirror-agent/config.yaml:ro
    environment:
      - MM_AGENT_API_KEY=${MM_AGENT_API_KEY}
```

Note: When running in Docker, the `restart_command` needs to be adjusted to either:
- Use `docker exec` to restart the MM container
- Use the Docker socket to control containers
- Call a webhook/script on the host

---

### PM2 Deployment

If you prefer using PM2 (same process manager as Magic Mirror).

#### Install with PM2

```bash
# Copy the binary
sudo cp magicmirror-agent-linux-arm64 /usr/local/bin/magicmirror-agent
sudo chmod +x /usr/local/bin/magicmirror-agent

# Create config
sudo mkdir -p /etc/magicmirror-agent
sudo cp config.example.yaml /etc/magicmirror-agent/config.yaml
# Edit config as needed

# Start with PM2
pm2 start /usr/local/bin/magicmirror-agent \
  --name "mm-agent" \
  -- --config /etc/magicmirror-agent/config.yaml

# Save PM2 configuration
pm2 save

# Enable PM2 startup
pm2 startup
```

#### PM2 Ecosystem File

Create `ecosystem.config.js`:

```javascript
module.exports = {
  apps: [
    {
      name: 'magicmirror-agent',
      script: '/usr/local/bin/magicmirror-agent',
      args: '--config /etc/magicmirror-agent/config.yaml',
      env: {
        MM_AGENT_API_KEY: 'your-api-key-here'
      },
      restart_delay: 5000,
      max_restarts: 10
    }
  ]
};
```

---

## Provider Installation

### Local Development

For development and testing:

```bash
make install-provider
```

This installs to `~/.terraform.d/plugins/local/SkylerGodfrey/magicmirror/<version>/<os>_<arch>/`

In your Terraform configuration:

```hcl
terraform {
  required_providers {
    magicmirror = {
      source  = "local/SkylerGodfrey/magicmirror"
      version = "0.1.0"
    }
  }
}
```

### Terraform Registry

To publish to the Terraform Registry (future):

1. Create a separate `terraform-provider-magicmirror` repository
2. Tag releases following semantic versioning
3. Sign releases with GPG
4. Register at registry.terraform.io

Once published:

```hcl
terraform {
  required_providers {
    magicmirror = {
      source  = "SkylerGodfrey/magicmirror"
      version = "~> 0.1"
    }
  }
}
```

---

## Network Configuration

### Local-Only Access

For maximum security, bind only to localhost:

```yaml
# config.yaml
server:
  host: "127.0.0.1"
  port: 8484
```

Run Terraform from the same device, or use SSH tunneling:

```bash
# Create tunnel from your machine
ssh -L 8484:localhost:8484 pi@192.168.1.50

# Then in another terminal, use localhost
export MM_HOST=localhost
terraform apply
```

### LAN Access

Allow connections from your local network:

```yaml
# config.yaml
server:
  host: "0.0.0.0"
  port: 8484

auth:
  enabled: true
  api_key: "strong-random-key"
```

Configure firewall (if enabled):

```bash
# UFW
sudo ufw allow from 192.168.1.0/24 to any port 8484

# iptables
sudo iptables -A INPUT -p tcp -s 192.168.1.0/24 --dport 8484 -j ACCEPT
```

### Reverse Proxy with TLS

For secure remote access, use nginx with Let's Encrypt.

#### Install Certbot and Nginx

```bash
sudo apt update
sudo apt install -y nginx certbot python3-certbot-nginx
```

#### Configure Nginx

Create `/etc/nginx/sites-available/magicmirror-agent`:

```nginx
server {
    listen 80;
    server_name mm-agent.yourdomain.com;

    location / {
        return 301 https://$server_name$request_uri;
    }
}

server {
    listen 443 ssl http2;
    server_name mm-agent.yourdomain.com;

    # SSL configuration (certbot will fill this in)
    ssl_certificate /etc/letsencrypt/live/mm-agent.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/mm-agent.yourdomain.com/privkey.pem;

    # Security headers
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Content-Type-Options nosniff;
    add_header X-Frame-Options DENY;

    # Rate limiting
    limit_req_zone $binary_remote_addr zone=api:10m rate=10r/s;

    location / {
        limit_req zone=api burst=20 nodelay;

        proxy_pass http://127.0.0.1:8484;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Enable the site and get certificate:

```bash
sudo ln -s /etc/nginx/sites-available/magicmirror-agent /etc/nginx/sites-enabled/
sudo nginx -t
sudo systemctl reload nginx

# Get SSL certificate
sudo certbot --nginx -d mm-agent.yourdomain.com
```

Update agent config to only listen locally:

```yaml
server:
  host: "127.0.0.1"
  port: 8484
```

Update Terraform provider to use HTTPS:

```hcl
provider "magicmirror" {
  host    = "mm-agent.yourdomain.com"
  port    = 443
  api_key = var.api_key
  # Note: Provider would need TLS support added
}
```

---

## Magic Mirror Setup Variations

### PM2 (Default)

Most Magic Mirror installations use PM2:

```yaml
# config.yaml
magicmirror:
  config_path: "/home/pi/MagicMirror/config/config.js"
  restart_command: "pm2 restart MagicMirror"
```

### Systemd Service

If MM is managed by systemd:

```yaml
# config.yaml
magicmirror:
  config_path: "/home/pi/MagicMirror/config/config.js"
  restart_command: "sudo systemctl restart magicmirror"
```

Grant the agent user sudo access for this command:

```bash
# /etc/sudoers.d/magicmirror-agent
pi ALL=(ALL) NOPASSWD: /bin/systemctl restart magicmirror
```

### Docker Magic Mirror

If Magic Mirror runs in Docker:

```yaml
# config.yaml
magicmirror:
  config_path: "/home/pi/magicmirror-config/config.js"
  restart_command: "docker restart magicmirror"
```

Or with docker-compose:

```yaml
restart_command: "docker-compose -f /home/pi/magicmirror/docker-compose.yml restart"
```

---

## High Availability

For critical displays, consider these options:

### Config Backup Before Changes

The agent writes to a temp file and does an atomic rename, but you can add extra protection:

```yaml
# Future feature: automatic backups
magicmirror:
  config_path: "/home/pi/MagicMirror/config/config.js"
  backup_before_write: true
  backup_dir: "/home/pi/MagicMirror/config/backups"
  max_backups: 10
```

### Health Monitoring

Create a simple health check script:

```bash
#!/bin/bash
# /usr/local/bin/check-mm-agent.sh

if ! curl -sf http://localhost:8484/health > /dev/null; then
    echo "Agent unhealthy, restarting..."
    systemctl restart magicmirror-agent
fi
```

Add to cron:

```bash
# Check every 5 minutes
*/5 * * * * /usr/local/bin/check-mm-agent.sh
```

---

## Backup and Recovery

### Backup Script

```bash
#!/bin/bash
# /usr/local/bin/backup-mm-config.sh

BACKUP_DIR="/home/pi/mm-backups"
DATE=$(date +%Y%m%d_%H%M%S)

mkdir -p "$BACKUP_DIR"

# Backup MM config
cp /home/pi/MagicMirror/config/config.js "$BACKUP_DIR/config.js.$DATE"

# Backup agent config
cp /etc/magicmirror-agent/config.yaml "$BACKUP_DIR/agent-config.yaml.$DATE"

# Keep only last 30 backups
ls -t "$BACKUP_DIR"/config.js.* | tail -n +31 | xargs -r rm
ls -t "$BACKUP_DIR"/agent-config.yaml.* | tail -n +31 | xargs -r rm
```

### Recovery

```bash
# List backups
ls -la /home/pi/mm-backups/

# Restore a specific backup
cp /home/pi/mm-backups/config.js.20240115_120000 /home/pi/MagicMirror/config/config.js
pm2 restart MagicMirror

# Re-import into Terraform state
terraform import magicmirror_module.clock <module-id>
```

---

## Updating

### Update the Agent

```bash
# On your development machine
git pull
make build-agent-arm64

# Deploy
make deploy-agent-full MM_HOST=192.168.1.50
```

### Update the Provider

```bash
make install-provider

# Re-init Terraform to pick up new version
cd your-terraform-config/
terraform init -upgrade
```

### Version Compatibility

| Agent Version | Provider Version | Notes |
|---------------|------------------|-------|
| 0.1.x | 0.1.x | Initial release |

The agent and provider should generally use matching minor versions. Patch versions should be backwards compatible.