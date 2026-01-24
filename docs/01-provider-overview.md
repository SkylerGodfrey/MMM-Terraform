# Terraform Provider for Magic Mirror - Overview

Creating a Terraform provider for Magic Mirror is a great infrastructure-as-code project. Here's an overview of the approach.

## Architecture Options

Magic Mirror is configured via a `config.js` file on the device. Since it doesn't have a REST API by default, you have a few options:

| Approach | Pros | Cons |
|----------|------|------|
| **SSH-based** | No extra software on MM device | Requires SSH access, file parsing |
| **Custom API agent** | Clean REST interface | Requires running additional service on MM |
| **Remote config sync** | Simple file management | Less dynamic, requires restart mechanism |

## Recommended Approach: SSH-based Provider

This is the most practical since it requires no changes to the Magic Mirror device.

### 1. Provider Structure (Go)

```
terraform-provider-magicmirror/
├── main.go
├── go.mod
├── internal/
│   └── provider/
│       ├── provider.go        # Provider configuration (SSH creds, host)
│       ├── resource_module.go # Manage MM modules
│       └── resource_config.go # Manage global config
```

### 2. Key Resources to Implement

- **`magicmirror_module`** - Add/configure modules (clock, weather, calendar, etc.)
- **`magicmirror_config`** - Global settings (port, language, units, etc.)

### 3. Example Terraform Usage (Goal)

```hcl
provider "magicmirror" {
  host        = "192.168.1.50"
  ssh_user    = "pi"
  ssh_key     = file("~/.ssh/id_rsa")
  config_path = "/home/pi/MagicMirror/config/config.js"
}

resource "magicmirror_config" "main" {
  port     = 8080
  language = "en"
  units    = "imperial"
}

resource "magicmirror_module" "clock" {
  module   = "clock"
  position = "top_left"
  config = {
    displaySeconds = true
    showPeriod     = true
  }
}

resource "magicmirror_module" "weather" {
  module   = "weather"
  position = "top_right"
  config = {
    weatherProvider = "openweathermap"
    apiKey          = var.openweather_api_key
    location        = "New York"
  }
}
```

## Getting Started

To build this provider, you'll need:

1. **Go module structure** with the Terraform Plugin Framework
2. **Provider configuration** for SSH connectivity
3. **A basic resource** (e.g., `magicmirror_module`) as a starting point

The provider would connect via SSH, read/write the `config.js` file, and optionally restart the MM service when configuration changes.
