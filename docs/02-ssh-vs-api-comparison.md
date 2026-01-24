# SSH vs API Agent Approach - Detailed Comparison

A detailed comparison of the SSH-based and custom API agent approaches for the Magic Mirror Terraform provider.

## SSH-Based Approach

### Pros

- **Zero footprint** - No additional software on the Magic Mirror device
- **Works with existing setups** - Any MM installation is immediately compatible
- **Leverage existing infrastructure** - Reuse SSH keys, known_hosts, bastion hosts
- **Full device access** - Can also install modules, restart services, check logs
- **Battle-tested protocol** - SSH libraries are mature and well-understood

### Cons

- **JavaScript parsing complexity** - `config.js` is actual JavaScript, not JSON. It can contain:
  ```js
  // Comments
  var config = {
    address: process.env.MM_ADDRESS || "localhost",
    modules: require("./modules.js"),  // imports
    customFunc: function() { ... }     // functions
  }
  ```
- **No built-in concurrency control** - Risk of race conditions if multiple writes occur
- **Connection overhead** - Each Terraform operation opens an SSH session
- **Stateful connections** - Harder to handle timeouts, reconnects, partial failures

### Risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| SSH credentials in Terraform state | High | Use SSH agent forwarding, limit key permissions |
| Config corruption from partial writes | High | Write to temp file, then atomic move |
| Unparseable JS constructs break provider | Medium | Document supported config patterns, validate before write |
| Manual edits cause state drift | Medium | Store hash of managed sections, warn on drift |
| Network interruption during write | Medium | Implement retry logic, use temp files |

---

## Custom API Agent Approach

### Pros

- **Clean contract** - Well-defined REST/gRPC interface with proper schemas
- **Validation layer** - Agent can validate config before applying, return meaningful errors
- **Proper concurrency** - Agent handles locking, queuing, transactions
- **Richer operations** - Could expose module installation, health checks, log streaming
- **Easier testing** - Mock the API for provider unit tests
- **Decoupled** - Provider doesn't need to understand config.js internals
- **Reusable** - Agent could be useful to the broader MM community (CLI tools, web UIs)

### Cons

- **Two codebases** - Must maintain both provider and agent, keep them compatible
- **Deployment complexity** - Agent needs to be installed, configured, kept running
- **Another failure point** - If agent crashes, no Terraform management until restored
- **Security burden** - Must implement authentication, possibly TLS
- **Version coupling** - Provider v2 might not work with Agent v1

### Risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| Agent service goes down | High | Systemd auto-restart, health monitoring |
| Unauthenticated API exposure | High | Require API key, bind to localhost or VPN only |
| Version incompatibility | Medium | Semantic versioning, version negotiation endpoint |
| Agent vulnerabilities | Medium | Minimal dependencies, security audits, auto-updates |
| Additional attack surface | Medium | Firewall rules, fail2ban, rate limiting |

---

## Decision Matrix

| Factor | SSH | API Agent |
|--------|-----|-----------|
| Initial setup effort | Lower | Higher |
| Ongoing maintenance | Medium | Higher |
| Reliability | Medium | Higher (if agent is stable) |
| Security complexity | Lower | Higher |
| Config parsing robustness | Lower | Higher |
| Community adoptability | Higher | Lower |
| Feature extensibility | Lower | Higher |

---

## Recommendation

**Start with SSH-based**, with these constraints:

1. **Restrict config.js format** - Document that Terraform-managed configs must be pure JSON-compatible (no functions, requires, or dynamic expressions)
2. **Manage a dedicated section** - Only manage modules added via Terraform, leave manual modules untouched
3. **Atomic writes** - Write to `.config.js.tmp`, validate, then `mv` to `config.js`

If you later find SSH limitations painful, the provider's resource schemas won't change—you'd only swap the backend implementation to use an API agent.
