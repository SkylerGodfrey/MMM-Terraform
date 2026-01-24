# Uninstall Guide

This guide covers how to remove the Magic Mirror Agent and Terraform Provider.

## Uninstall the Agent (Magic Mirror Device)

Run these commands on your Magic Mirror device (Raspberry Pi):

### 1. Stop and Disable the Service

```bash
sudo systemctl stop magicmirror-agent
sudo systemctl disable magicmirror-agent
```

### 2. Remove the Service File

```bash
sudo rm /etc/systemd/system/magicmirror-agent.service
sudo systemctl daemon-reload
```

### 3. Remove the Binary

```bash
sudo rm /usr/local/bin/magicmirror-agent
```

### 4. Remove Configuration (Optional)

Only do this if you want to remove all configuration, including your API key:

```bash
sudo rm -rf /etc/magicmirror-agent
```

### One-Liner

To remove everything at once:

```bash
sudo systemctl stop magicmirror-agent && \
sudo systemctl disable magicmirror-agent && \
sudo rm -f /etc/systemd/system/magicmirror-agent.service && \
sudo systemctl daemon-reload && \
sudo rm -f /usr/local/bin/magicmirror-agent && \
sudo rm -rf /etc/magicmirror-agent
```

---

## Uninstall the Terraform Provider (Workstation)

Run these commands on the machine where you run Terraform:

### Remove the Provider Binary

```bash
rm -rf ~/.terraform.d/plugins/local/SkylerGodfrey/magicmirror
```

### Clean Up Terraform State (Optional)

If you have existing Terraform configurations using this provider:

```bash
cd your-terraform-config-directory

# Remove the provider lock
rm -f .terraform.lock.hcl

# Remove cached providers
rm -rf .terraform/providers
```

---

## Verify Uninstallation

### On the Magic Mirror Device

```bash
# Should return "not found"
which magicmirror-agent

# Should return "could not be found"
systemctl status magicmirror-agent

# Should not exist
ls /etc/magicmirror-agent
```

### On Your Workstation

```bash
# Should not exist
ls ~/.terraform.d/plugins/local/SkylerGodfrey/magicmirror
```

---

## Reinstalling

To reinstall after uninstalling, follow the [Quick Start](README.md#quick-start) guide.
