# Configuration

ELIDA is configured via `configs/elida.yaml` or environment variables.

## Full YAML Reference

```yaml
# Proxy listener
listen: ":8080"

# Single backend (use this OR the backends block, not both)
backend: "http://localhost:11434"

# Multi-backend routing (see docs/ROUTING.md for details)
backends:
  ollama:
    url: "http://localhost:11434"
    type: ollama
    default: true
  openai:
    url: "https://api.openai.com"
    type: openai
    models: ["gpt-*", "o1-*"]
    api_key: ""  # Optional: inject API key server-side (enables keyless clients)
  anthropic:
    url: "https://api.anthropic.com"
    type: anthropic
    models: ["claude-*"]
    api_key: ""  # Optional: inject API key server-side
  groq:
    url: "https://api.groq.com/openai/v1"
    type: groq
    models: ["llama-*", "mixtral-*"]
    api_key: ""  # Optional: use GROQ_API_KEY env var instead
routing:
    methods:
      - header
      - model
      - path
      - default

# Session management
session:
  timeout: 5m
  header: "X-Session-ID"
  generate_if_missing: true
  store: "memory"  # "memory" or "redis"

  # Derive session identity from the request body when no X-Session-ID
  # header is sent (see "Body-Derived Session Identity" below)
  derive_from:
    openai_user: true   # use the OpenAI `user` field (default true)
    body_path: ""        # optional dot-path, e.g. "metadata.conversation_id"

  # Kill block configuration
  kill_block:
    # "duration"          — Block for a specific time after kill
    # "until_hour_change" — Block until the clock hour changes
    # "permanent"         — Block until server restart
    mode: "duration"
    duration: 30m

# Control API and dashboard
control:
  listen: ":9090"
  enabled: true
  auth:
    enabled: true
    api_key: "your-control-api-key"  # Or use ELIDA_CONTROL_API_KEY env var

# Proxy authentication (optional - secures the proxy endpoint)
proxy:
  auth:
    enabled: true
    api_key: "your-proxy-api-key"  # Or use ELIDA_PROXY_API_KEY env var
    trusted_networks: []           # CIDRs whose direct peers skip the API-key check

# Policy engine
policy:
  enabled: true
  mode: "enforce"        # "enforce" or "audit"
  preset: "standard"     # "minimal", "standard", "strict", "mcp", or "coding-agent"
  capture_flagged: true
  rules:
    - name: "high_request_count"
      type: "request_count"
      threshold: 100
      severity: "warning"
  suppress_rules: []     # Rule names to drop after merge (preset, custom, or generated)

# Storage (session history and capture)
storage:
  enabled: true
  capture_mode: "flagged_only"    # "flagged_only" or "all"
  max_capture_size: 10000         # Max bytes per request/response body
  max_captured_per_session: 100   # Max captured pairs per session

# WebSocket / Voice
websocket:
  enabled: false
  voice_sessions:
    enabled: true
    max_concurrent: 5
    protocols:
      - openai_realtime
      - deepgram
      - elevenlabs

# TLS
tls:
  enabled: false
  cert_file: ""
  key_file: ""

# Redis (when session store is "redis")
redis:
  addr: "localhost:6379"
  password: ""
  db: 0

# OpenTelemetry
telemetry:
  enabled: false
  exporter: "otlp"          # "otlp", "stdout", or "none"
  endpoint: ""              # OTLP endpoint (e.g., "localhost:4317")
  service_name: "elida"
  insecure: true
  capture_content: "none"   # "none", "flagged", or "all"
  max_body_size: 4096       # Truncation limit for captured bodies
```

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `configs/elida.yaml` | Path to config file |
| `-listen` | — | Override listen address (e.g. `:8082`) |
| `-validate` | `false` | Validate config and exit |
| `-version` | `false` | Print version and exit |

Priority: CLI flag > environment variable > config file.

## Environment Variables

All configuration can be overridden with environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `ELIDA_LISTEN` | `:8080` | Proxy listen address |
| `ELIDA_BACKEND` | `http://localhost:11434` | Backend URL |
| `ELIDA_CONTROL_LISTEN` | `:9090` | Control API address |
| `ELIDA_SESSION_STORE` | `memory` | Session store: `memory` or `redis` |
| `ELIDA_SESSION_TIMEOUT` | `5m` | Session timeout duration |
| `ELIDA_POLICY_ENABLED` | `false` | Enable policy engine |
| `ELIDA_POLICY_MODE` | `enforce` | Policy mode: `enforce` or `audit` |
| `ELIDA_POLICY_PRESET` | — | Policy preset: `minimal`, `standard`, `strict` |
| `ELIDA_STORAGE_ENABLED` | `false` | Enable SQLite storage |
| `ELIDA_STORAGE_CAPTURE_MODE` | `flagged_only` | Capture mode: `flagged_only` or `all` |
| `ELIDA_WEBSOCKET_ENABLED` | `false` | Enable WebSocket proxy |
| `ELIDA_TLS_ENABLED` | `false` | Enable TLS/HTTPS |
| `ELIDA_TLS_CERT_FILE` | — | Path to TLS certificate |
| `ELIDA_TLS_KEY_FILE` | — | Path to TLS private key |
| `ELIDA_TELEMETRY_ENABLED` | `false` | Enable OpenTelemetry |
| `ELIDA_REDIS_ADDR` | `localhost:6379` | Redis address |
| `ELIDA_REDIS_PASSWORD` | — | Redis password |
| `ELIDA_REDIS_DB` | `0` | Redis database number |
| `ELIDA_CONTROL_API_KEY` | — | API key for control API auth (auto-enables auth) |
| `ELIDA_PROXY_API_KEY` | — | API key for proxy auth (auto-enables auth) |

## Proxy Authentication

ELIDA supports optional API key authentication on the proxy endpoint to prevent unauthorized access.

### Configuration

```yaml
proxy:
  auth:
    enabled: true
    api_key: "your-secret-key"  # Or use ELIDA_PROXY_API_KEY env var
```

### Supported Auth Methods

| Method | Header | Example |
|--------|--------|---------|
| ELIDA API Key | `X-Elida-API-Key` | `X-Elida-API-Key: your-secret-key` |
| Bearer Token | `Authorization` | `Authorization: Bearer your-secret-key` |

### Security Features

- **Constant-time comparison** — Uses `crypto/subtle.ConstantTimeCompare` to prevent timing attacks
- **Header stripping** — `X-Elida-API-Key` is stripped before forwarding to backend (not leaked)
- **Health bypass** — `/health`, `/healthz`, `/ready`, `/readyz` bypass auth for load balancer probes

### Trusted Networks (`proxy.auth.trusted_networks`)

A CIDR allowlist whose **direct peers** skip the API-key check entirely. This lets un-keyed auxiliary agent calls (compression, title generation) work on a trusted network while the wider LAN still needs the key.

```yaml
proxy:
  auth:
    enabled: true
    api_key: "your-proxy-api-key"
    trusted_networks:
      - "127.0.0.1/32"    # loopback
      - "::1/128"         # loopback (IPv6)
      - "172.16.0.0/12"   # e.g. a private/container network
```

- **Direct peer only** — the trust decision looks at the TCP connection's remote address only. It never consults `X-Forwarded-For` or `X-Real-IP`, so a client can't spoof its way past auth by setting those headers.
- **Fail closed at startup** — an invalid CIDR in `trusted_networks` fails config validation rather than being silently skipped.
- **Empty list = no bypass** — if `trusted_networks` is unset or empty, nobody is exempt; auth behaves exactly as if the feature didn't exist.
- **Docker gateway gotcha** — a containerized ELIDA sees requests from a host-side client through the Docker bridge, so `r.RemoteAddr` is the bridge gateway (typically `172.17.0.1`), not the real client. Include the bridge subnet (e.g. `172.17.0.0/16`) if you want host-side calls exempted — but that also exempts every other container on the same bridge network.
- **Reverse-proxy deployment caveat** — trust is decided by the *direct* peer. If ELIDA sits behind a local reverse proxy (nginx, Envoy, a load balancer), all external traffic arrives at ELIDA with the reverse proxy's source IP — trusting loopback (or that proxy's IP) would exempt every external client, not just internal callers. Only use `trusted_networks` when ELIDA itself is the network-facing listener; behind a reverse proxy, rely on `proxy.auth.api_key` instead (and have the proxy enforce access control upstream).

A per-request `slog.Debug` line (`"proxy auth bypassed for trusted network client"`) is emitted whenever a request actually uses the bypass, so the effect is visible when debug logging is enabled.

### Backend API Key Injection (Keyless Clients)

ELIDA can inject API keys server-side, enabling keyless clients (SBC pattern):

```yaml
backends:
  openai:
    url: "https://api.openai.com"
    type: openai
    api_key: "sk-..."  # Injected into requests automatically
```

Clients connect to ELIDA without any API key. ELIDA injects the correct auth header based on backend type:
- **Anthropic**: `x-api-key: <key>`
- **OpenAI/Groq/Mistral**: `Authorization: Bearer <key>`

This is useful for:
- Public demos with rate limiting
- Internal services without credential distribution
- Multi-tenant setups with per-backend keys

## Policy Direction Split

ELIDA splits content policy rules by direction to prevent false positives from LLM conversation history while still catching real threats.

### How It Works

| Direction | Severity | Action | Purpose |
|-----------|----------|--------|---------|
| **Response** (AI output) | Critical | Block/Terminate | AI generating dangerous content is a real threat |
| **Request** (user input) | Critical | Flag | Conversation history may contain matching patterns; risk ladder escalates |

Request-side flags score **10.0 points** (critical severity) on the risk ladder. Repeated violations escalate automatically:

| Risk Score | Action |
|------------|--------|
| 5 | Warn |
| 15 | Throttle |
| 30 | Block |
| 50 | Terminate |

### Rule Suppression and Observe Mode

Two knobs shape which rules run and how much weight they carry, on top of
the local-overrides-default merge described above (a custom rule with the
same `name` as a preset rule replaces it).

| Field | Type | Description |
|-------|------|--------------|
| `policy.suppress_rules` | `[]string` | Rule names to drop after the merge. Works on preset rules, custom rules, and generated circuit-breaker rules alike. |
| `rules[].observe` | `bool` | Marks a single rule observe-only: it still flags and captures, but its action is forced to `flag` and it contributes nothing to the risk ladder. |

```yaml
policy:
  preset: standard
  suppress_rules: [destructive_file_ops, compound_anomaly]
  rules:
    - name: shell_execution
      type: content_match
      target: response
      patterns: ["bash\\s+-c\\s+"]
      severity: warning
      action: flag
      observe: true   # flag + capture only, never escalates the risk ladder
```

`mode: audit` is a true dry run on top of this: rule actions don't enforce
and the risk ladder is clamped to observe/warn, so audit mode can never
throttle, block, or terminate — even if individual rules or the risk
ladder would otherwise escalate.

### The `coding-agent` Preset

`policy.preset: coding-agent` is tuned for trusted coding agents (Claude
Code, Hermes, Cursor) whose legitimate output contains `bash -c`, `sudo`,
`rm -rf`, `curl | sh`, and whose tool loops look like high-rate bursts to
anomaly detectors. Structural rules (dangerous tool names/arguments,
credential-access tool calls, rate limits) enforce; content and
statistical heuristics (shell/privilege/destructive/exfil patterns,
prompt injection, PII, `rate_anomaly`, `compound_anomaly`) run in observe
mode. Nothing in the preset terminates a session. See
[docs/policy-rules-reference.md](policy-rules-reference.md) for the full
rule list.

```yaml
policy:
  preset: coding-agent
  circuit_breaker:
    enabled: true
    max_tool_fanout: 100   # agents legitimately expose 30+ tools
```

### Allowlisted Tools

Tools that bypass request-side content scanning. When the latest assistant message contains only allowlisted tools, the request skips policy checks entirely.

```yaml
policy:
  trust:
    allowlisted_tools:
      - "Read"
      - "Glob"
      - "Grep"
      - "Edit"
      - "Write"
      - "Agent"
```

Tools like `Bash` are intentionally excluded — they can execute dangerous commands and should be scanned.

## Session ID Behavior

ELIDA resolves a session ID per request, in order of precedence:

1. **`X-Session-ID` header** — if present, used verbatim.
2. **Body-derived identity** (`session.derive_from`) — see below.
3. **Client-IP + backend fallback** — `client-<hash>-<backend>`; requests from the same client to the same backend are grouped into one session automatically when neither of the above applies.

```bash
# Use explicit session ID
curl -H "X-Session-ID: my-agent-task-123" http://localhost:8080/api/generate ...

# Response includes the session ID
< X-Session-ID: my-agent-task-123
```

### Body-Derived Session Identity (`session.derive_from`)

When no `X-Session-ID` header is sent, ELIDA can derive a stable session ID from the JSON request body, so one conversation keeps one session even if routing sends its requests to different backends (failover, load balancing, retries). Derived IDs deliberately contain no backend component — failover never splits a conversation into separate sessions, and the kill-switch stays per-conversation instead of per-host.

```yaml
session:
  derive_from:
    openai_user: true   # derive from the OpenAI `user` field (default: true)
    body_path: ""       # optional dot-path, e.g. "metadata.conversation_id"
```

Precedence when deriving from the body:

1. `session.derive_from.body_path`, if configured and the path resolves to a non-empty string in the body — takes precedence over the `user` field.
2. `session.derive_from.openai_user` — the standard OpenAI `user` field, on by default.
3. Neither applies (or both are disabled) — falls through to the client-IP + backend fallback above.

Derived values are formatted as `user-<value>` when the value is short and contains only `[A-Za-z0-9._:-]`, or `user-<16 hex chars>` (a SHA-256-based hash) otherwise — so arbitrary or long `user`/body values never leak verbatim into the session ID.

Set `openai_user: false` and leave `body_path` empty to disable body-derived identity entirely and always use the client-IP + backend fallback when no `X-Session-ID` header is sent.

## Settings Hierarchy (Layered Configuration)

ELIDA uses a VS Code-style layered settings system. Settings are merged in order, with later layers overriding earlier ones:

```
┌─────────────────────────────────────────────────────────┐
│  Layer 3: settings.yaml (UI overrides) — highest       │
│           Hot-reloaded, no restart needed               │
├─────────────────────────────────────────────────────────┤
│  Layer 2: Environment Variables                         │
│           Override YAML at startup                      │
├─────────────────────────────────────────────────────────┤
│  Layer 1: elida.yaml (base config) — lowest            │
│           Loaded at startup                             │
└─────────────────────────────────────────────────────────┘
```

### How It Works

1. **`configs/elida.yaml`** — Base configuration loaded at startup
2. **Environment variables** — Override YAML values (e.g., `ELIDA_POLICY_MODE=audit`)
3. **`configs/settings.yaml`** — UI overrides, created when you save settings in the dashboard

### Example

```yaml
# configs/elida.yaml (base)
policy:
  enabled: true
  mode: enforce
  preset: standard
```

```bash
# Environment override
export ELIDA_POLICY_MODE=audit
```

```yaml
# configs/settings.yaml (UI override, auto-generated)
policy:
  mode: enforce  # Overrides env var back to enforce
  custom_rules:
    - name: block_competitor_mentions
      type: content_match
      patterns: ["competitor-name"]
      action: block
```

**Result:** Policy enabled, enforce mode (UI wins), standard preset, plus custom rule.

### Dynamic Reload (Hot-Reload)

Changes made via the Settings UI are applied instantly — no restart required. The policy engine reloads its configuration atomically while preserving active session state.

```bash
# Save settings via API
curl -X PUT http://localhost:9090/control/settings \
  -H "Content-Type: application/json" \
  -d '{"policy":{"mode":"audit"}}'

# Response
{"status":"saved","message":"Settings applied instantly (no restart required)"}
```

### Custom Rules

Custom rules defined in the UI are appended to the preset rules (they don't replace them). Rules use [RE2 regex syntax](https://github.com/google/re2/wiki/Syntax).

```yaml
# configs/settings.yaml
policy:
  custom_rules:
    - name: pii_ssn_strict
      type: content_match
      target: both
      patterns:
        - "\\b\\d{3}-\\d{2}-\\d{4}\\b"
      severity: critical
      action: block
      description: "Block SSN patterns"
```
