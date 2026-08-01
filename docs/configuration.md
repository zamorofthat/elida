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
    model: ""    # Optional: model id substituted in ONLY when failover lands here
routing:
    methods:
      - header
      - model
      - path
      - default

# Failover (see "Failover" below; disabled by default)
failover:
  enabled: false
  max_retries: 2
  retry_delay: 100ms
  fallback_order: []      # e.g. [openai, anthropic] — set explicitly; see recommendation below
  preserve_model: true

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

## Secrets in Config: `${ENV}` Expansion and Auto Provider Keys

Config values can reference environment variables directly, and backend API keys can be picked
up automatically by convention, so secrets never need to be committed alongside `elida.yaml`.

### `${VAR}` expansion

`${IDENTIFIER}` references are expanded from the environment in five fields: `backend`,
`backends.<name>.url`, `backends.<name>.api_key`, `proxy.auth.api_key`, and
`control.auth.api_key`. Nowhere else — in particular, policy rule `patterns` are never expanded,
so a regex like `${HOME}$` is never mistaken for a secret reference.

```yaml
backends:
  openai:
    url: "https://api.openai.com"
    type: openai
    api_key: "${OPENAI_API_KEY}"
```

An unset variable is left as the literal `${VAR}` string (not silently emptied) and logs a
warning at startup, so a missing secret is visible immediately instead of failing mysteriously
downstream.

### Auto provider keys

If `backends.<name>.api_key` is left empty (or omitted), ELIDA looks it up automatically, in order:

1. `<NAME>_API_KEY` — the backend's own name, upper-cased, with non-alphanumerics replaced by
   `_` (e.g. backend `eu-llm` → `EU_LLM_API_KEY`).
2. The conventional variable for the backend's `type`: `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or
   `MISTRAL_API_KEY`.

An explicit `api_key` (literal or `${VAR}`-expanded) always wins over both.

### Recommended pattern: keys in env, config in git

Keep `elida.yaml` free of secrets and safe to commit, with keys supplied entirely by the
environment:

```yaml
# elida.yaml — safe to commit
backends:
  primary:
    url: "https://api.openai.com"
    type: openai
    default: true
    # no api_key: picked up automatically from OPENAI_API_KEY
  nemotron:
    url: "https://integrate.api.nvidia.com/v1"
    type: openai
    # no api_key: picked up automatically from NEMOTRON_API_KEY
```

```bash
# .env / deployment secrets — never committed
OPENAI_API_KEY=sk-...
NEMOTRON_API_KEY=nvapi-...
```

See [`.env.example`](../.env.example) for the full list of variables ELIDA reads.

## Failover

**Disabled by default.** Failover was previously constructible only in tests and had no effect
on real traffic; it is now wired up automatically from config at startup — set
`failover.enabled: true` to turn it on.

> **Limitation:** failover currently applies only to non-streaming requests; streaming (SSE)
> responses are returned from the primary backend as-is, with no failover on error.

```yaml
failover:
  enabled: true          # default: false
  max_retries: 2         # default: 2 — max failover hops before giving up
  retry_delay: 100ms     # default: 0 — delay before trying the next backend
  fallback_order:        # order to try backends in; see recommendation below
    - primary
    - secondary
  preserve_model: true   # default: true
```

When a request fails (5xx, timeout, connection error, or a 429 without `Retry-After`), ELIDA
retries it against the next backend in `fallback_order`. The retry preserves the original
request's conversation (its messages, and any system prompt) so the fallback backend sees the
same content the client sent — not just an empty prompt. When the session has recorded
conversation history (via the session APIs), that recorded history is used instead, giving the
fallback backend the fuller context accumulated across the session rather than just the single
failed request.

**Recommendation: always set `fallback_order` explicitly.** Backends not listed in
`fallback_order` are still eligible — they're simply tried last, in unspecified (map iteration)
order. For predictable failover, list every backend you want considered, in priority order.

### `backends.<name>.model` — failover-only model rewrite

```yaml
backends:
  primary:
    url: "https://api.mymodel.com"
    type: openai
    models: ["gemma"]
    default: true
  fallback:
    url: "https://api.openai.com"
    type: openai
    model: "gpt-4"   # substituted in on failover only; normal routing is untouched
```

`backends.<name>.model` only takes effect when a request fails over onto that backend — it never
changes normal (non-failover) routing, nor the model a directly-addressed backend receives.

Different backends/providers use disjoint model catalogs (a `gemma` request can't be sent to an
Anthropic backend as-is), so failover must resolve a compatible model id for the target before
forwarding. Decision order:

1. **Explicit substitution** — `backends.<name>.model`, if set, always wins.
2. **Glob-compatible passthrough** — if the target's `models` globs match the original model id
   unchanged, it's forwarded as-is.
3. **Remap table** — otherwise, ELIDA's built-in model-family remap table (e.g. `gpt-4` ↔
   `claude-3-opus-20240229`) is consulted; the result is only accepted if it also matches the
   target's `models` globs, or the target declares no `models` at all (nothing to validate
   against).
4. **Skip loudly** — if nothing above resolves to a valid model for the target, that backend is
   skipped entirely (no request is ever sent to it), and failover moves on to the next candidate
   in `fallback_order`. This prevents forwarding an untranslated model id to a backend that
   can't understand it (previously a 400 from the target backend).

If every candidate is skipped or fails, the client receives a `502` with a JSON body
(`{"error":"failover_exhausted","message":"All backends unavailable"}`) instead of the last
attempted backend's raw response.

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
