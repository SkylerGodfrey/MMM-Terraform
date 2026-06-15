# Claude Code Context for MMM-Terraform

## Architecture

Two Go binaries that work together:

**`magicmirror-agent/`** — HTTP server that runs on the Pi as a systemd service
- Reads and writes `/home/sgodfrey/MagicMirror/config/config.js`
- REST API on port 8484, authenticated via `Authorization: Bearer <key>` or `MM_AGENT_API_KEY` env var
- Config at `/etc/magicmirror-agent/config.yaml`
- Service definition: `deploy/magicmirror-agent.service`

**`terraform-provider-magicmirror/`** — Terraform plugin that talks to the agent
- Installed locally to `~/.terraform.d/plugins/local/SkylerGodfrey/magicmirror/`
- Resources: `magicmirror_config` (global settings), `magicmirror_module` (per-module config)
- Client code: `internal/provider/client.go`

**`my-mirror/`** — Live Terraform config for the actual mirror (not a template)

## Key Files

```
terraform-provider-magicmirror/internal/provider/
├── client.go           ← HTTP client for the agent API
├── provider.go         ← Provider schema and configuration
├── resource_config.go  ← magicmirror_config resource
└── resource_module.go  ← magicmirror_module resource

magicmirror-agent/
├── main.go
├── internal/api/       ← HTTP handlers
└── internal/config/    ← Config loading

my-mirror/main.tf       ← live mirror declaration
```

## Common Commands

| Task | Command |
|------|---------|
| Build both binaries | `make build` |
| Install provider locally | `make install-provider` |
| Deploy agent to Pi | `make deploy-agent` |
| Deploy + install on Pi | `make deploy-agent-full` |
| Run all tests | `make test` |
| Format + vet | `make lint` |
| Build release binaries | `make release-binaries` |

Override SSH target: `make deploy-agent MM_HOST=10.0.0.5 MM_USER=ubuntu`

## Build Notes

- Cross-compile agent for Pi (ARM64): `make build-agent-arm64`
- Provider is installed with `make install-provider` before running `terraform apply`
- Go modules: `github.com/SkylerGodfrey/terraform-provider-magicmirror` and `github.com/SkylerGodfrey/magicmirror-agent`
- Provider address: `registry.terraform.io/SkylerGodfrey/magicmirror`

## Conventions

- Never commit API keys; `my-mirror/terraform.tfvars` is gitignored for sensitive vars
- Terraform state is local: `my-mirror/terraform.tfstate`
- After `magicmirror_config` or `magicmirror_module` changes, the agent triggers `pm2 restart MagicMirror`
- See `_docs/architecture.md` in the workspace root for the full system diagram
- Tickets tracked in YouTrack under epic **HOM-21**: https://sgodfrey.youtrack.cloud/issue/HOM-21
