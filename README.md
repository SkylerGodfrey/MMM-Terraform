# MMM-Terraform

Manage your [Magic Mirror](https://magicmirror.builders/) configuration with Terraform.

This project provides:
- **magicmirror-agent**: A REST API that runs on your Magic Mirror device
- **terraform-provider-magicmirror**: A Terraform provider that communicates with the agent

## Prerequisites

- [Terraform 1.0+](https://terraform.io/downloads)
- A Magic Mirror device (Raspberry Pi) with SSH access
- Magic Mirror installed and running

## Quick Start

### Option A: One-Line Install (Recommended)

On your Magic Mirror device (Raspberry Pi), run:

```bash
curl -fsSL https://raw.githubusercontent.com/SkylerGodfrey/MMM-Terraform/main/scripts/install-agent.sh | bash
```

This will:
- Download the pre-built binary for your platform
- Generate a secure API key (save it!)
- Create the configuration file
- Install and start the systemd service

**Save the API key** displayed during installation - you'll need it for Terraform.

Then skip to [Step 4: Install the Terraform Provider](#4-install-the-terraform-provider).

### Option B: Build from Source

Requires [Go 1.21+](https://golang.org/dl/).

#### 1. Clone and Build

```bash
git clone https://github.com/SkylerGodfrey/MMM-Terraform.git
cd MMM-Terraform

# Build everything
make build

# Or build individually
make build-provider      # Builds the Terraform provider
make build-agent-arm64   # Builds the agent for Raspberry Pi (64-bit)
make build-agent-arm     # Builds the agent for Raspberry Pi (32-bit)
```

#### 2. Generate an API Key

```bash
make gen-api-key
# Output: Generated API key:
# a1b2c3d4e5f6...  (save this!)
```

#### 3. Deploy the Agent to Your Magic Mirror

```bash
# Deploy files to the device
make deploy-agent MM_HOST=192.168.1.50 MM_USER=pi
```

Then SSH into your device and complete the installation:

```bash
ssh pi@192.168.1.50

# Move files into place
sudo mv /tmp/magicmirror-agent /usr/local/bin/
sudo chmod +x /usr/local/bin/magicmirror-agent
sudo mkdir -p /etc/magicmirror-agent
sudo mv /tmp/magicmirror-agent-config.yaml /etc/magicmirror-agent/config.yaml

# Edit the config and add your API key
sudo nano /etc/magicmirror-agent/config.yaml
```

Update the config file:

```yaml
server:
  host: "0.0.0.0"  # Allow remote access
  port: 8484

magicmirror:
  config_path: "/home/pi/MagicMirror/config/config.js"
  restart_command: "pm2 restart MagicMirror"

auth:
  enabled: true
  api_key: "your-generated-api-key-here"
```

Install and start the systemd service:

```bash
sudo mv /tmp/magicmirror-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable magicmirror-agent
sudo systemctl start magicmirror-agent

# Verify it's running
sudo systemctl status magicmirror-agent
```

### 4. Install the Terraform Provider

**Option A: Download pre-built binary**

```bash
# Download the latest release for your platform
# Replace OS and ARCH as needed: linux/darwin, amd64/arm64
OS=darwin
ARCH=arm64
VERSION=v0.1.0

curl -fsSL "https://github.com/SkylerGodfrey/MMM-Terraform/releases/download/${VERSION}/terraform-provider-magicmirror_${OS}_${ARCH}" \
  -o terraform-provider-magicmirror

# Install to Terraform plugins directory
mkdir -p ~/.terraform.d/plugins/local/SkylerGodfrey/magicmirror/0.1.0/${OS}_${ARCH}
mv terraform-provider-magicmirror ~/.terraform.d/plugins/local/SkylerGodfrey/magicmirror/0.1.0/${OS}_${ARCH}/
chmod +x ~/.terraform.d/plugins/local/SkylerGodfrey/magicmirror/0.1.0/${OS}_${ARCH}/terraform-provider-magicmirror
```

**Option B: Build from source (requires Go)**

```bash
make install-provider
```

This builds and installs the provider to `~/.terraform.d/plugins/` for local development.

### 5. Create Your Terraform Configuration

Create a new directory for your configuration:

```bash
mkdir my-mirror
cd my-mirror
```

Create `main.tf`:

```hcl
terraform {
  required_providers {
    magicmirror = {
      source  = "local/SkylerGodfrey/magicmirror"
      version = "0.1.0"
    }
  }
}

provider "magicmirror" {
  host    = "192.168.1.50"
  port    = 8484
  api_key = var.api_key
}

variable "api_key" {
  type      = string
  sensitive = true
}

# Global configuration
resource "magicmirror_config" "main" {
  address     = "0.0.0.0"
  port        = 8080
  language    = "en"
  time_format = 12
  units       = "imperial"
}

# Clock module
resource "magicmirror_module" "clock" {
  module   = "clock"
  position = "top_left"

  config = jsonencode({
    displaySeconds = true
    showPeriod     = true
  })
}

# Weather module
resource "magicmirror_module" "weather" {
  module   = "weather"
  position = "top_right"

  config = jsonencode({
    weatherProvider = "openweathermap"
    type            = "current"
    location        = "New York"
    apiKey          = "your-openweathermap-key"
  })
}
```

### 6. Apply Your Configuration

```bash
# Initialize Terraform
terraform init

# Preview changes
terraform plan -var="api_key=your-generated-api-key"

# Apply changes
terraform apply -var="api_key=your-generated-api-key"
```

## Automated Deployment

If your user has passwordless sudo on the Magic Mirror device:

```bash
make deploy-agent-full MM_HOST=192.168.1.50 MM_USER=pi
```

This copies files, installs them, and starts the service in one command.

## Verifying the Deployment

Check if the agent is running:

```bash
make check-agent MM_HOST=192.168.1.50
```

Or manually test the API:

```bash
# Health check (no auth required)
curl http://192.168.1.50:8484/health

# Get current config (requires auth)
curl -H "Authorization: Bearer your-api-key" \
  http://192.168.1.50:8484/api/v1/config
```

## Project Structure

```
MMM-Terraform/
├── Makefile                        # Build and deploy automation
├── docs/                           # Documentation
├── examples/                       # Example Terraform configurations
├── deploy/
│   └── magicmirror-agent.service   # Systemd service file
├── magicmirror-agent/              # API agent (runs on MM device)
│   ├── main.go
│   ├── config.example.yaml
│   └── internal/
│       ├── api/                    # HTTP handlers
│       ├── config/                 # Agent configuration
│       └── mmconfig/               # MM config file management
└── terraform-provider-magicmirror/ # Terraform provider
    ├── main.go
    └── internal/provider/
        ├── provider.go             # Provider definition
        ├── client.go               # API client
        ├── resource_module.go      # magicmirror_module resource
        └── resource_config.go      # magicmirror_config resource
```

## Make Targets

| Target | Description |
|--------|-------------|
| `make build` | Build both provider and agent |
| `make build-provider` | Build only the Terraform provider |
| `make build-agent-arm64` | Build agent for Raspberry Pi 64-bit |
| `make build-agent-arm` | Build agent for Raspberry Pi 32-bit |
| `make install-provider` | Install provider for local Terraform use |
| `make deploy-agent` | Copy agent files to Magic Mirror device |
| `make deploy-agent-full` | Deploy and install agent automatically |
| `make check-agent` | Verify agent is running on device |
| `make gen-api-key` | Generate a secure API key |
| `make clean` | Remove built binaries |
| `make help` | Show all available targets |

## Configuration Reference

### Agent Configuration (`/etc/magicmirror-agent/config.yaml`)

| Setting | Description | Default |
|---------|-------------|---------|
| `server.host` | IP to bind to (`0.0.0.0` for remote access) | `127.0.0.1` |
| `server.port` | API port | `8484` |
| `magicmirror.config_path` | Path to MM config.js | `/home/pi/MagicMirror/config/config.js` |
| `magicmirror.restart_command` | Command to restart MM | `pm2 restart MagicMirror` |
| `auth.enabled` | Require API key | `true` |
| `auth.api_key` | The API key (or use `MM_AGENT_API_KEY` env var) | - |

### Provider Configuration

| Attribute | Description | Default |
|-----------|-------------|---------|
| `host` | Agent hostname/IP | `localhost` |
| `port` | Agent port | `8484` |
| `api_key` | API key for authentication | - |
| `timeout` | HTTP timeout in seconds | `30` |

### Module Resource (`magicmirror_module`)

| Attribute | Description | Required |
|-----------|-------------|----------|
| `module` | Module name (e.g., `clock`, `weather`) | Yes |
| `position` | Screen position (see below) | No |
| `header` | Header text above module | No |
| `disabled` | Disable the module | No |
| `classes` | Additional CSS classes | No |
| `config` | Module config as JSON string | No |

Valid positions: `top_bar`, `top_left`, `top_center`, `top_right`, `upper_third`, `middle_center`, `lower_third`, `bottom_left`, `bottom_center`, `bottom_right`, `bottom_bar`, `fullscreen_above`, `fullscreen_below`

## Troubleshooting

### Agent won't start

Check the logs:
```bash
sudo journalctl -u magicmirror-agent -f
```

Common issues:
- Config file not found: Verify path in systemd service
- Permission denied: Check file ownership and `ReadWritePaths` in service file
- Port in use: Change port in config

### Terraform can't connect

1. Verify agent is running: `make check-agent MM_HOST=...`
2. Check firewall allows port 8484
3. Verify API key matches in agent config and Terraform

### Config changes not appearing

The provider updates `config.js` but doesn't restart Magic Mirror by default. Add a `null_resource` with a provisioner or use the API's restart endpoint.

## Security Considerations

- **API Key**: Always use a strong, randomly generated API key
- **Network**: Bind to `127.0.0.1` if only accessing locally, or use a VPN/firewall
- **Terraform State**: The API key is stored in Terraform state; use remote state with encryption
- **HTTPS**: For production, consider putting the agent behind a reverse proxy with TLS

## License

See [LICENSE](LICENSE) for details.