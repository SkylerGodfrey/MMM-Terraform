# Releasing

This document describes how to create releases and what the GitHub Actions workflow automates.

## Table of Contents

- [Quick Release](#quick-release)
- [Version Numbering](#version-numbering)
- [What the Workflow Does](#what-the-workflow-does)
- [Release Artifacts](#release-artifacts)
- [Manual Release (Without GitHub Actions)](#manual-release-without-github-actions)
- [Testing Before Release](#testing-before-release)
- [Troubleshooting](#troubleshooting)

---

## Quick Release

To create a new release:

```bash
# 1. Ensure you're on main and up to date
git checkout main
git pull origin main

# 2. Create and push a version tag
git tag v0.1.0
git push origin v0.1.0
```

That's it. GitHub Actions will automatically:
- Build binaries for all platforms
- Create a GitHub Release
- Upload all artifacts with checksums

---

## Version Numbering

This project follows [Semantic Versioning](https://semver.org/):

```
v{MAJOR}.{MINOR}.{PATCH}[-{PRERELEASE}]
```

| Version | When to Use |
|---------|-------------|
| `v0.1.0` | Initial development release |
| `v0.1.1` | Bug fixes (backwards compatible) |
| `v0.2.0` | New features (backwards compatible) |
| `v1.0.0` | First stable release (API contract locked) |
| `v2.0.0` | Breaking changes |
| `v1.0.0-beta.1` | Pre-release for testing |
| `v1.0.0-rc.1` | Release candidate |

**Pre-release versions** (containing `-`) are automatically marked as pre-release on GitHub and won't be downloaded by the install script's `--version latest` option.

---

## What the Workflow Does

The GitHub Actions workflow (`.github/workflows/release.yml`) is triggered when you push a tag matching `v*`.

### Build Matrix

The workflow builds binaries for these platforms:

**Agent (`magicmirror-agent`)**

| OS | Architecture | Binary Name | Use Case |
|----|--------------|-------------|----------|
| Linux | amd64 | `magicmirror-agent-linux-amd64` | x86 Linux servers |
| Linux | arm64 | `magicmirror-agent-linux-arm64` | Raspberry Pi 4/5 (64-bit OS) |
| Linux | arm (v7) | `magicmirror-agent-linux-arm` | Raspberry Pi 3 (32-bit OS) |
| macOS | amd64 | `magicmirror-agent-darwin-amd64` | Intel Macs |
| macOS | arm64 | `magicmirror-agent-darwin-arm64` | Apple Silicon Macs |

**Provider (`terraform-provider-magicmirror`)**

| OS | Architecture | Binary Name | Use Case |
|----|--------------|-------------|----------|
| Linux | amd64 | `terraform-provider-magicmirror_linux_amd64` | Linux workstations, CI |
| Linux | arm64 | `terraform-provider-magicmirror_linux_arm64` | ARM Linux |
| macOS | amd64 | `terraform-provider-magicmirror_darwin_amd64` | Intel Macs |
| macOS | arm64 | `terraform-provider-magicmirror_darwin_arm64` | Apple Silicon Macs |
| Windows | amd64 | `terraform-provider-magicmirror_windows_amd64.exe` | Windows |

### Workflow Steps

```
┌─────────────────────────────────────────────────────────────────┐
│  Push tag v0.1.0                                                │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  Job: build-agent (runs in parallel)                            │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ Matrix: 5 platform combinations                          │   │
│  │ • Checkout code                                          │   │
│  │ • Setup Go 1.21                                          │   │
│  │ • Run: go mod tidy && go build                           │   │
│  │ • Upload binary as artifact                              │   │
│  └─────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  Job: build-provider (runs in parallel)                         │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ Matrix: 5 platform combinations                          │   │
│  │ • Checkout code                                          │   │
│  │ • Setup Go 1.21                                          │   │
│  │ • Run: go mod tidy && go build                           │   │
│  │ • Upload binary as artifact                              │   │
│  └─────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  Job: release (waits for build jobs)                            │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ • Download all artifacts                                 │   │
│  │ • Generate SHA256 checksums                              │   │
│  │ • Create GitHub Release                                  │   │
│  │ • Upload all binaries + checksums.txt                    │   │
│  │ • Auto-generate release notes from commits               │   │
│  └─────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  GitHub Release created at:                                     │
│  https://github.com/SkylerGodfrey/MMM-Terraform/releases/tag/v0.1.0   │
└─────────────────────────────────────────────────────────────────┘
```

### Build Flags

Binaries are built with these Go flags:

```bash
go build -trimpath -ldflags="-s -w -X main.version=v0.1.0"
```

| Flag | Purpose |
|------|---------|
| `-trimpath` | Remove local file paths from binary (reproducible builds) |
| `-s` | Omit symbol table (smaller binary) |
| `-w` | Omit DWARF debug info (smaller binary) |
| `-X main.version=...` | Embed version string in binary |

---

## Release Artifacts

After a successful release, the following files are available:

```
https://github.com/SkylerGodfrey/MMM-Terraform/releases/download/v0.1.0/
├── magicmirror-agent-linux-amd64
├── magicmirror-agent-linux-arm64
├── magicmirror-agent-linux-arm
├── magicmirror-agent-darwin-amd64
├── magicmirror-agent-darwin-arm64
├── terraform-provider-magicmirror_linux_amd64
├── terraform-provider-magicmirror_linux_arm64
├── terraform-provider-magicmirror_darwin_amd64
├── terraform-provider-magicmirror_darwin_arm64
├── terraform-provider-magicmirror_windows_amd64.exe
└── checksums.txt
```

### Verifying Downloads

Users can verify downloads using the checksums:

```bash
# Download binary and checksums
curl -LO https://github.com/SkylerGodfrey/MMM-Terraform/releases/download/v0.1.0/magicmirror-agent-linux-arm64
curl -LO https://github.com/SkylerGodfrey/MMM-Terraform/releases/download/v0.1.0/checksums.txt

# Verify
sha256sum -c checksums.txt --ignore-missing
# magicmirror-agent-linux-arm64: OK
```

---

## Manual Release (Without GitHub Actions)

If you need to build releases locally:

```bash
# Build all release binaries
make release-binaries

# Output is in dist/
ls -la dist/
# magicmirror-agent-linux-amd64
# magicmirror-agent-linux-arm64
# magicmirror-agent-linux-arm
# magicmirror-agent-darwin-amd64
# magicmirror-agent-darwin-arm64
# terraform-provider-magicmirror_linux_amd64
# terraform-provider-magicmirror_linux_arm64
# terraform-provider-magicmirror_darwin_amd64
# terraform-provider-magicmirror_darwin_arm64
# terraform-provider-magicmirror_windows_amd64.exe
# checksums.txt
```

Then manually create a GitHub release and upload the files, or distribute them another way.

---

## Testing Before Release

Before creating a release tag, test thoroughly:

### 1. Build and Test Locally

```bash
# Build for your platform
make build

# Run tests
make test

# Lint
make lint
```

### 2. Test on Target Device

```bash
# Build for Raspberry Pi
make build-agent-arm64

# Deploy to test device
make deploy-agent MM_HOST=192.168.1.50

# Or use the full deploy
make deploy-agent-full MM_HOST=192.168.1.50

# Verify it's working
make check-agent MM_HOST=192.168.1.50
```

### 3. Test the Install Script

To test the install script without a release, serve binaries locally:

```bash
# Terminal 1: Build and serve binaries
make release-binaries
cd dist && python3 -m http.server 8000

# Terminal 2: On test device, run install script with custom URL
# (Requires adding --url flag to install script - see docs)
```

Or copy files manually and test the script's config generation:

```bash
# Copy binary to device
scp dist/magicmirror-agent-linux-arm64 pi@192.168.1.50:/tmp/magicmirror-agent
scp scripts/install-agent.sh pi@192.168.1.50:/tmp/

# On the device, install manually then test
ssh pi@192.168.1.50
sudo mv /tmp/magicmirror-agent /usr/local/bin/
sudo chmod +x /usr/local/bin/magicmirror-agent
# Continue with manual config setup...
```

### 4. Test Terraform Provider

```bash
# Install provider locally
make install-provider

# Create a test configuration
cd examples/
terraform init
terraform plan -var="api_key=test-key"
```

---

## Troubleshooting

### Workflow Not Triggering

Ensure your tag matches the pattern `v*`:

```bash
# Correct
git tag v0.1.0

# Wrong - won't trigger workflow
git tag 0.1.0
git tag release-0.1.0
```

### Build Failures

Check the Actions tab on GitHub for logs:
`https://github.com/SkylerGodfrey/MMM-Terraform/actions`

Common issues:
- **Go module errors**: Run `go mod tidy` locally and commit changes
- **Syntax errors**: Run `make build` locally first
- **Missing dependencies**: Check go.mod and go.sum are committed

### Release Already Exists

If a release for the tag already exists:

```bash
# Delete the tag locally and remotely
git tag -d v0.1.0
git push origin :refs/tags/v0.1.0

# Delete the release on GitHub (web UI)
# Then re-tag and push
git tag v0.1.0
git push origin v0.1.0
```

### Wrong Binaries Released

If you need to update binaries for an existing release:

1. Delete the release on GitHub (keeps the tag)
2. Push an empty commit to re-trigger, or
3. Delete and recreate the tag

```bash
# Option: Delete tag and recreate
git tag -d v0.1.0
git push origin :refs/tags/v0.1.0
git tag v0.1.0
git push origin v0.1.0
```

---

## Checklist for Releases

Before tagging a release:

- [ ] All tests pass (`make test`)
- [ ] Code is formatted (`make fmt`)
- [ ] No linter warnings (`make lint`)
- [ ] CHANGELOG updated (if you have one)
- [ ] README reflects any new features
- [ ] Tested on target device (Raspberry Pi)
- [ ] Version number follows semver

After release:

- [ ] Verify release appears on GitHub
- [ ] Verify all artifacts are uploaded
- [ ] Test install script with new version
- [ ] Announce release (if applicable)
