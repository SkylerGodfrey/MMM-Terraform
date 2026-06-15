# Magic Mirror Terraform Provider & Agent
# Makefile for building, installing, and deploying

# Version
VERSION ?= 0.1.0

# Go settings - try to find Go in common locations
# Override with: make build GO=/path/to/go
GO_PATHS := $(shell which go 2>/dev/null) \
            /usr/local/go/bin/go \
            /opt/homebrew/bin/go \
            /home/linuxbrew/.linuxbrew/bin/go \
            $(HOME)/go/bin/go \
            $(HOME)/.local/go/bin/go

GO := $(or $(shell which go 2>/dev/null), \
           $(firstword $(wildcard $(GO_PATHS))))

# If GO is still empty, set a default that will trigger the check
ifeq ($(GO),)
    GO := go
endif

GOFLAGS := -trimpath -ldflags="-s -w"

# Provider settings
PROVIDER_NAME := terraform-provider-magicmirror
PROVIDER_DIR := terraform-provider-magicmirror
PROVIDER_BIN := $(PROVIDER_DIR)/$(PROVIDER_NAME)

# Agent settings
AGENT_NAME := magicmirror-agent
AGENT_DIR := magicmirror-agent
AGENT_BIN := $(AGENT_DIR)/$(AGENT_NAME)

# Detect OS and architecture for provider installation
OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
ARCH := $(shell uname -m)
ifeq ($(ARCH),x86_64)
	ARCH := amd64
endif
ifeq ($(ARCH),aarch64)
	ARCH := arm64
endif
ifeq ($(ARCH),arm64)
	ARCH := arm64
endif

# Terraform plugin directory
TF_PLUGIN_DIR := ~/.terraform.d/plugins/local/SkylerGodfrey/magicmirror/$(VERSION)/$(OS)_$(ARCH)

# Magic Mirror device settings (override with environment variables or make args)
MM_HOST ?= raspberrypi.local
MM_USER ?= sgodfrey
MM_SSH := $(MM_USER)@$(MM_HOST)

# ============================================================================
# Default target
# ============================================================================

.PHONY: all
all: build

# ============================================================================
# Go check
# ============================================================================

.PHONY: check-go
check-go: ## Verify Go is installed
	@if ! command -v $(GO) >/dev/null 2>&1; then \
		echo ""; \
		echo "ERROR: Go not found!"; \
		echo ""; \
		echo "Please install Go from https://golang.org/dl/"; \
		echo ""; \
		echo "Common installation methods:"; \
		echo "  macOS (Homebrew): brew install go"; \
		echo "  macOS (pkg):      Download from https://golang.org/dl/"; \
		echo "  Linux (apt):      sudo apt install golang-go"; \
		echo "  Linux (snap):     sudo snap install go --classic"; \
		echo ""; \
		echo "After installing, you may need to:"; \
		echo "  1. Add Go to your PATH in ~/.bashrc or ~/.zshrc:"; \
		echo "     export PATH=\$$PATH:/usr/local/go/bin"; \
		echo "  2. Restart your terminal or run: source ~/.zshrc"; \
		echo ""; \
		echo "Or specify the Go path directly:"; \
		echo "  make build GO=/usr/local/go/bin/go"; \
		echo ""; \
		exit 1; \
	fi
	@echo "Using Go: $(GO) ($$($(GO) version))"

# ============================================================================
# Build targets
# ============================================================================

.PHONY: build
build: build-provider build-agent ## Build both provider and agent

.PHONY: build-provider
build-provider: check-go ## Build the Terraform provider
	@echo "Building Terraform provider..."
	cd $(PROVIDER_DIR) && $(GO) mod tidy && $(GO) build $(GOFLAGS) -o $(PROVIDER_NAME)
	@echo "Built: $(PROVIDER_BIN)"

.PHONY: build-agent
build-agent: check-go ## Build the agent for current platform
	@echo "Building Magic Mirror agent..."
	cd $(AGENT_DIR) && $(GO) mod tidy && $(GO) build $(GOFLAGS) -o $(AGENT_NAME)
	@echo "Built: $(AGENT_BIN)"

.PHONY: build-agent-arm64
build-agent-arm64: check-go ## Build the agent for Raspberry Pi (ARM64)
	@echo "Building Magic Mirror agent for ARM64..."
	cd $(AGENT_DIR) && $(GO) mod tidy && GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -o $(AGENT_NAME)-linux-arm64
	@echo "Built: $(AGENT_DIR)/$(AGENT_NAME)-linux-arm64"

.PHONY: build-agent-arm
build-agent-arm: check-go ## Build the agent for Raspberry Pi (ARM 32-bit)
	@echo "Building Magic Mirror agent for ARM..."
	cd $(AGENT_DIR) && $(GO) mod tidy && GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(GOFLAGS) -o $(AGENT_NAME)-linux-arm
	@echo "Built: $(AGENT_DIR)/$(AGENT_NAME)-linux-arm"

# ============================================================================
# Install targets
# ============================================================================

.PHONY: install-provider
install-provider: build-provider ## Install the provider locally for Terraform
	@echo "Installing provider to $(TF_PLUGIN_DIR)..."
	@mkdir -p $(TF_PLUGIN_DIR)
	@cp $(PROVIDER_BIN) $(TF_PLUGIN_DIR)/
	@echo "Provider installed. Use this in your Terraform config:"
	@echo ""
	@echo '  terraform {'
	@echo '    required_providers {'
	@echo '      magicmirror = {'
	@echo '        source  = "local/SkylerGodfrey/magicmirror"'
	@echo '        version = "$(VERSION)"'
	@echo '      }'
	@echo '    }'
	@echo '  }'

# ============================================================================
# Deploy targets (to Magic Mirror device)
# ============================================================================

.PHONY: deploy-agent
deploy-agent: build-agent-arm64 ## Deploy the agent to the Magic Mirror device
	@echo "Deploying agent to $(MM_HOST)..."
	scp $(AGENT_DIR)/$(AGENT_NAME)-linux-arm64 $(MM_SSH):/tmp/$(AGENT_NAME)
	scp $(AGENT_DIR)/config.example.yaml $(MM_SSH):/tmp/magicmirror-agent-config.yaml
	scp deploy/magicmirror-agent.service $(MM_SSH):/tmp/magicmirror-agent.service
	@echo ""
	@echo "Files copied. Run the following on the device to complete installation:"
	@echo ""
	@echo "  ssh $(MM_SSH)"
	@echo "  sudo mv /tmp/$(AGENT_NAME) /usr/local/bin/"
	@echo "  sudo chmod +x /usr/local/bin/$(AGENT_NAME)"
	@echo "  sudo mkdir -p /etc/magicmirror-agent"
	@echo "  sudo mv /tmp/magicmirror-agent-config.yaml /etc/magicmirror-agent/config.yaml"
	@echo "  sudo mv /tmp/magicmirror-agent.service /etc/systemd/system/"
	@echo "  sudo systemctl daemon-reload"
	@echo "  sudo systemctl enable magicmirror-agent"
	@echo "  sudo systemctl start magicmirror-agent"

.PHONY: deploy-agent-full
deploy-agent-full: build-agent-arm64 ## Deploy and install agent (requires sudo on remote)
	@echo "Deploying and installing agent on $(MM_HOST)..."
	scp $(AGENT_DIR)/$(AGENT_NAME)-linux-arm64 $(MM_SSH):/tmp/$(AGENT_NAME)
	scp $(AGENT_DIR)/config.example.yaml $(MM_SSH):/tmp/magicmirror-agent-config.yaml
	scp deploy/magicmirror-agent.service $(MM_SSH):/tmp/magicmirror-agent.service
	ssh $(MM_SSH) 'sudo mv /tmp/$(AGENT_NAME) /usr/local/bin/ && \
		sudo chmod +x /usr/local/bin/$(AGENT_NAME) && \
		sudo mkdir -p /etc/magicmirror-agent && \
		if [ ! -f /etc/magicmirror-agent/config.yaml ]; then \
			sudo mv /tmp/magicmirror-agent-config.yaml /etc/magicmirror-agent/config.yaml; \
		else \
			echo "Config exists, not overwriting"; \
			rm /tmp/magicmirror-agent-config.yaml; \
		fi && \
		sudo mv /tmp/magicmirror-agent.service /etc/systemd/system/ && \
		sudo systemctl daemon-reload && \
		sudo systemctl enable magicmirror-agent && \
		sudo systemctl restart magicmirror-agent'
	@echo "Agent deployed and started on $(MM_HOST)"

# ============================================================================
# Module deploy targets (HOM-70)
# ============================================================================
# Modules live in sibling workspace directories. We rsync the source tree
# (excluding live data, secrets, dev/, and node_modules) and run npm install
# on the device.

MODULES_DIR ?= /home/sgodfrey/MagicMirror/modules

.PHONY: deploy-recipesage
deploy-recipesage: ## Deploy MMM-RecipeSage to the Magic Mirror device
	@echo "Deploying MMM-RecipeSage to $(MM_HOST)..."
	rsync -av --delete \
		--exclude '.git/' \
		--exclude 'node_modules/' \
		--exclude 'dev/' \
		--exclude 'selfhost/' \
		--exclude '.DS_Store' \
		../MMM-RecipeSage/ $(MM_SSH):$(MODULES_DIR)/MMM-RecipeSage/
	ssh $(MM_SSH) 'cd $(MODULES_DIR)/MMM-RecipeSage && npm install --omit=dev --no-audit --no-fund'
	@echo "MMM-RecipeSage deployed. Agent will pm2-restart MagicMirror after the next terraform apply."

# ============================================================================
# Development targets
# ============================================================================

.PHONY: test
test: test-provider test-agent ## Run all tests

.PHONY: test-provider
test-provider: check-go ## Run provider tests
	@echo "Running provider tests..."
	cd $(PROVIDER_DIR) && $(GO) test -v ./...

.PHONY: test-agent
test-agent: check-go ## Run agent tests
	@echo "Running agent tests..."
	cd $(AGENT_DIR) && $(GO) test -v ./...

.PHONY: fmt
fmt: check-go ## Format Go code
	@echo "Formatting code..."
	cd $(PROVIDER_DIR) && $(GO) fmt ./...
	cd $(AGENT_DIR) && $(GO) fmt ./...

.PHONY: vet
vet: check-go ## Run go vet
	@echo "Running go vet..."
	cd $(PROVIDER_DIR) && $(GO) vet ./...
	cd $(AGENT_DIR) && $(GO) vet ./...

.PHONY: lint
lint: fmt vet ## Run all linters

# ============================================================================
# Release targets
# ============================================================================

.PHONY: release-binaries
release-binaries: check-go ## Build release binaries for all platforms
	@echo "Building release binaries..."
	@mkdir -p dist

	@echo "Building agent binaries..."
	cd $(AGENT_DIR) && GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o ../dist/$(AGENT_NAME)-linux-amd64
	cd $(AGENT_DIR) && GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -o ../dist/$(AGENT_NAME)-linux-arm64
	cd $(AGENT_DIR) && GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(GOFLAGS) -o ../dist/$(AGENT_NAME)-linux-arm
	cd $(AGENT_DIR) && GOOS=darwin GOARCH=amd64 $(GO) build $(GOFLAGS) -o ../dist/$(AGENT_NAME)-macos-amd64
	cd $(AGENT_DIR) && GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) -o ../dist/$(AGENT_NAME)-macos-arm64

	@echo "Building provider binaries..."
	cd $(PROVIDER_DIR) && GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o ../dist/$(PROVIDER_NAME)_linux_amd64
	cd $(PROVIDER_DIR) && GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -o ../dist/$(PROVIDER_NAME)_linux_arm64
	cd $(PROVIDER_DIR) && GOOS=darwin GOARCH=amd64 $(GO) build $(GOFLAGS) -o ../dist/$(PROVIDER_NAME)_macos_amd64
	cd $(PROVIDER_DIR) && GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) -o ../dist/$(PROVIDER_NAME)_macos_arm64
	cd $(PROVIDER_DIR) && GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -o ../dist/$(PROVIDER_NAME)_windows_amd64.exe

	@echo "Generating checksums..."
	cd dist && shasum -a 256 * > checksums.txt

	@echo "Release binaries built in dist/"
	@ls -la dist/

# ============================================================================
# Utility targets
# ============================================================================

.PHONY: clean
clean: ## Remove built binaries
	@echo "Cleaning..."
	rm -f $(PROVIDER_BIN)
	rm -f $(AGENT_BIN)
	rm -f $(AGENT_DIR)/$(AGENT_NAME)-linux-*
	rm -rf dist/

.PHONY: gen-api-key
gen-api-key: ## Generate a secure API key
	@echo "Generated API key:"
	@openssl rand -hex 32

.PHONY: check-agent
check-agent: ## Check if agent is running on the device
	@echo "Checking agent status on $(MM_HOST)..."
	@ssh $(MM_SSH) 'systemctl status magicmirror-agent' || true
	@echo ""
	@echo "Testing API..."
	@curl -s http://$(MM_HOST):8484/health 2>/dev/null && echo " - Agent is responding" || echo " - Agent is not responding"

# ============================================================================
# Help
# ============================================================================

.PHONY: help
help: ## Show this help message
	@echo "Magic Mirror Terraform Provider & Agent"
	@echo ""
	@echo "Usage: make [target] [VAR=value]"
	@echo ""
	@echo "Variables:"
	@echo "  GO         Path to Go binary (auto-detected, or specify manually)"
	@echo "  MM_HOST    Magic Mirror hostname (default: raspberrypi.local)"
	@echo "  MM_USER    SSH user (default: pi)"
	@echo "  VERSION    Version number (default: 0.1.0)"
	@echo ""
	@echo "Examples:"
	@echo "  make build                          # Build with auto-detected Go"
	@echo "  make build GO=/usr/local/go/bin/go  # Build with specific Go path"
	@echo "  make deploy-agent MM_HOST=10.0.0.5  # Deploy to specific host"
	@echo ""
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
