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

# Policy engine
policy:
  enabled: true
  mode: "enforce"        # "enforce" or "audit"
  preset: "standard"     # "minimal", "standard", or "strict"
  capture_flagged: true
  rules:
    - name: "high_request_count"
      type: "request_count"
      threshold: 100
      severity: "warning"

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

Sessions are identified by the `X-Session-ID` header. If not provided, ELIDA generates one automatically.

```bash
# Use explicit session ID
curl -H "X-Session-ID: my-agent-task-123" http://localhost:8080/api/generate ...

# Response includes the session ID
< X-Session-ID: my-agent-task-123
```

For Claude Code, ELIDA uses client IP-based session tracking, so all requests from the same IP are grouped into a single session automatically.

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
