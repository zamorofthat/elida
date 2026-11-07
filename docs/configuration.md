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
  redaction:
    enabled: true                 # PII/secret redaction on captured bodies (default true)
    redact_private_ips: false     # also redact loopback/RFC1918/link-local IPs (default false)
    patterns: []                  # custom patterns, appended after the built-in set

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

## Redaction

ELIDA redacts sensitive data out of captured request/response bodies before
they're written to session history or emitted as OCSF events. Redaction is
on by default (`storage.redaction.enabled: true`).

### JSON-Aware Body Redaction

Captured bodies are redacted structurally, not by scanning raw bytes:

- **JSON object/array**: parsed, then only *string values* are scanned for
  sensitive patterns — numeric fields (`created`, `n_params`, token counts,
  etc.) are left untouched. Parsing uses `json.Number` so large integers
  (e.g. 19-digit snowflake IDs) survive byte-exact instead of being rounded
  through `float64`.
- **SSE streams**: each `data: ` line's JSON payload is redacted the same
  way; non-JSON payloads (`data: [DONE]`) and non-`data:` lines (`event:`,
  `id:`, `retry:`, blank lines) pass through unredacted-structure but still
  redacted with the raw-text redactor; original CRLF line endings are
  preserved.
- **Anything else** (non-JSON, non-SSE body): falls back to raw-text
  redaction, same as before.

**Guarantee**: a body that was valid JSON (whole-body or per SSE `data:`
line) comes out as valid JSON. Re-marshaling compacts whitespace and may
reorder object keys — the guarantee is *parseable and semantically intact*,
not byte-identical formatting.

Free-text fields that are never JSON documents — policy violation
`matched_text`, voice transcript text, TTS text, instruction-registry
content — are intentionally redacted with the raw-text redactor instead,
since there's no structure to preserve.

### Built-in Pattern Changes

Two of the built-in patterns were tightened to cut false positives observed
in production capture:

- **Credit card**: a 13-16 digit candidate is only redacted if it passes
  the Luhn checksum. Non-Luhn digit runs (order numbers, phone-adjacent
  digits, etc.) are left alone.
- **Phone (US)**: only matches when the digits carry phone formatting —
  parens, dashes, dots, or a `+1` prefix (e.g. `(555) 123-4567`,
  `555-123-4567`). A bare 10-digit number embedded in other text (a unix
  timestamp, an ID) is no longer treated as a phone number.

### Private IP Handling

By default, loopback (`127.0.0.1`), RFC1918 private (`10.0.0.0/8`,
`172.16.0.0/12`, `192.168.0.0/16`), and link-local (`169.254.0.0/16`) IPs
are **not** redacted — in practice these are dev/internal addresses, not
PII. Set `storage.redaction.redact_private_ips: true` to restore the old
behavior (redact every IP-shaped match) for deployments where internal
addressing is itself sensitive.

```yaml
storage:
  redaction:
    redact_private_ips: true
```

Custom patterns (`storage.redaction.patterns`) are unaffected by any of the
above — they're appended after the built-in set and applied with the raw
regex/replacement you configure. See [docs/telco-controls.md](telco-controls.md#4-pii-redaction)
for the full built-in pattern table and custom-pattern examples.

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

> **Note:** This key ships empty by default — no tools bypass scanning unless explicitly configured. The example below uses Claude Code's tool names; other agents (Hermes, Cursor, custom agents) must supply their own `allowlisted_tools` list matching their available tools. An empty allowlist is the secure default.

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

The allowlist applies to the regex policy engine only. Semantic injection detection (`decision.*`) ignores it. An allowlisted tool's output, such as a `Read` of a README that carries an injection, is still assessed. That untrusted tool output is exactly what semantic detection exists to score.

## Semantic Injection Detection (`decision.*`)

The `decision` block in `configs/elida.yaml` documents every key inline. This
section covers the rules that span several keys. Known limits of the detector
itself are listed in [Semantic injection detection](semantic-detection.md#limitations).

### `decision.require_inline`

`true` fails startup unless the computed capability is `inline`: the model
loaded and verified, and the build has accelerated inference kernels. Those
kernels are gated to linux/amd64 built with `GOEXPERIMENT=simd`, so every other
target (arm64 included) reports `async_only` and fails the gate. With the gate
off, an `async_only` build never tries the inline lane: eligible windows go
straight to the async lane (admission reason `capability_async_only`). An unmet gate
fails startup whatever `decision.required` says, including when the model does
not load; the error names the architecture, whether SIMD kernels are compiled
in, and which builds can be inline.

The key is inert while semantic detection is off (`decision.enabled: false` or
`decision.mode: disabled`): no model is loaded, startup succeeds, and config
validation warns that `require_inline` has no effect. `/control/decision`
echoes the configured value as `require_inline`.

### `decision.max_concurrency` (default 4)

The physical inference worker pool, split into an inline lane and an async lane
that never share slots, so async backlog cannot take a worker the request path
needs. Async workers are 0 at 1, otherwise `max(1, max_concurrency / 4)`;
inline takes the rest:

| `max_concurrency` | inline | async |
|---|---|---|
| 1 | 1 | 0 (async continuation disabled) |
| 2 | 1 | 1 |
| 4 (default) | 3 | 1 |
| 8 | 6 | 2 |
| 16 | 12 | 4 |

At 1, a window that misses the inline lane is a coverage gap recorded with its
real capacity reason (`no_worker_available`, `deadline_spent`,
`inline_budget_spent`). A slot is not one CPU: one embedded inference used
about 4.7 CPUs of work per wall-clock second on an 8-core machine.

### Modes and when enforcement is refused

- **`enforce` requires a matching threshold set.** `decision.mode: enforce`
  with a `decision.threshold_set` that does not match the loaded model's
  threshold set is always a startup error, whatever `decision.required` says.
  Choose the matching threshold set, or run `audit`. `audit` with a mismatch
  starts, and `/control/decision` reports `threshold_set_matches: false`.
- **"Strict" enforcement means `decision.mode: enforce`.** Only `enforce` lets
  semantic violations drive the risk ladder to block or terminate.
  An inline finding is recorded before the request is forwarded, and the proxy
  then re-checks the ladder, so the request that carried the finding meets the
  ladder's action (the same `risk_threshold_exceeded` 403 as any later
  request). An async finding affects later requests only.
  `decision.inline_admission.broad_strict_mode` only widens which messages may
  try the inline lane; it does not change what a finding does.
- **`policy.mode: audit` caps `enforce` to `audit`.** Validation warns. The
  runner re-derives its effective mode whenever settings are applied, so a
  later `policy.mode: audit` saved through the dashboard or settings API caps
  `enforce` immediately, and switching back to `enforce` lifts the cap. (The
  WARN about a stale cap remains only where no decision configurer is wired,
  i.e. in tests.) While capped, `GET /control/decision` reports
  `mode: enforce`, `effective_mode: audit` and a `reason` that includes
  `capped by policy.mode=audit`. The policy engine's own mode still decides
  whether the ladder acts.
- **No policy engine caps `audit` and `enforce` to `shadow`.** With
  `policy.enabled: false` nothing can be recorded. Validation warns, the
  startup log gives the reason, and `/control/decision` reports
  `effective_mode: shadow` with reason `policy engine disabled`. Shadow is
  never capped, so it carries no such reason.
- **Audit evidence flags the session.** An `audit` finding (and any
  `injection_elevated` event, in `audit` or `enforce`) is recorded as an
  evidence-only violation. It adds no risk, but it makes the session flagged,
  so the session is persisted as a flagged session on every response and its
  captured content follows the flagged-content capture and export path. The
  evidence is visible on purpose. It never raises the session's maximum
  severity. Every export marks it: OTEL span events and logs carry
  `evidence_only`, OCSF findings carry `elida.evidence_only` with `severity_id`
  1 (Informational), and SQLite `violation_detected` rows carry
  `evidence_only`. The exported `elida.violations.max_severity` ignores it.
  Semantic violations also carry their decision metadata (never content) in
  every export; see [Telemetry](semantic-detection.md#telemetry).

### Changing decision settings at runtime

Only two groups of `decision` keys change at runtime, through the dashboard or
`PUT /control/settings` (and `/control/settings/local`); everything else needs
`elida.yaml` and a restart:

| Runtime (applied on save) | Restart-only (read-only at runtime) |
|---|---|
| `decision.mode` | `decision.threshold_set` |
| `decision.preprocessing.max_input_bytes` | `decision.elevated_threshold` |
| `decision.preprocessing.max_analysis_bytes` | `decision.inline_timeout_ms` |
| `decision.preprocessing.max_representations` | `decision.max_concurrency` |
| `decision.preprocessing.max_decode_depth` | `decision.max_inline_tokens` |
| `decision.preprocessing.max_expansion_ratio` | `decision.max_inline_windows` |
| | `decision.max_async_windows` |
| | `decision.async_queue_size` |

`decision.enabled`, `required`, `require_inline`, `provider`, `model_path` and
`endpoint` are not settings keys at all. `/control/decision` reports the
restart-only values in force under `limits`.

Guardrails:

- **Restart-only changes are refused.** A save that sets a restart-only key
  to anything but its value in force (the `elida.yaml` value, shown under
  `limits` at `/control/decision`) returns 400 with `problems` naming the
  key, and nothing is saved; the same body without that key succeeds.
  Omitting a key is never a change. A stale restart-only value already in
  `settings.yaml` (for example after `elida.yaml` changed) never takes effect
  and is logged at WARN at startup. It never blocks a save: a save may keep
  it (a dashboard round trip), drop it (a partial save that omits it), or
  set it back to the value in force; only a third value is refused.
- **`enforce` needs confirmation.** Changing `decision.mode` to `enforce`
  requires `?confirm=enforce` on the request; a UI dialog alone is not enough.
  It is refused while the saved `policy.mode` is `audit`, and when the
  configured threshold set does not match the loaded model.
- **Values are checked before saving.** Preprocessing values outside their
  ceilings, and any decision change while semantic detection is not running
  (`decision.enabled: false`, `decision.mode: disabled` at startup, or a
  model that did not load), are refused with 400.
- **The runner re-caps on every save.** Each save (and each reset) applies the
  merged `decision` section with the saved `policy.mode`, so the effective
  mode always follows the rules in
  [Modes and when enforcement is refused](#modes-and-when-enforcement-is-refused).
  An async result uses the mode in force when it is delivered, not when it
  was queued; a result delivered while the mode is `disabled` is dropped and
  counted in `async_dropped_disabled`.
- **`settings.yaml` is applied at startup.** Its `decision` section is applied
  once after the settings store loads, through the same checks. `enforce`
  with a mismatched threshold set there fails startup, as it does in
  `elida.yaml`; any other refused value is logged at WARN and the
  `elida.yaml` value stays in force. (The `policy` section of `settings.yaml`
  is still applied only by a save, so the startup cap uses `elida.yaml`'s
  `policy.mode`.) An `enforce` saved in `settings.yaml` while that
  `policy.mode` is `audit` is applied capped to `audit`, not refused; a later
  `policy.mode: enforce` save (or a reset) lifts the cap without
  `?confirm=enforce`. That `enforce` was confirmed when it was saved; a later
  `elida.yaml` change does not re-confirm it. The same holds for any cap:
  lifting it needs no confirmation.
- **`settings.yaml` holds only overrides.** A save stores a `decision` key
  only when its value differs from `elida.yaml`'s; a key equal to the
  `elida.yaml` value is dropped (the merged value is the same). A dashboard
  save, which sends every value, therefore never pins `elida.yaml`'s values.
  At startup every runtime `decision` key that `settings.yaml` overrides is
  logged at WARN (`settings.yaml overrides elida.yaml for a decision key`)
  with both values, so an `elida.yaml` edit that a stored override shadows
  is visible. Save the key from the dashboard, or reset settings, to change
  it.
- **Every change is logged.** Each changed key writes one INFO line,
  `decision setting changed`, with `key`, `previous` and `new` values,
  `remote_addr`, and `authenticated` (whether control API authentication is
  on, so the request passed it). Values are configuration only, never content
  or credentials.
- **Reserved rule names.** A custom rule may not be named
  `semantic_injection` or `injection_elevated` (the names semantic detection
  records under). A save that introduces such a name (a new rule, or a rename)
  is refused with 400 naming it. A rule with such a name that is already saved
  (by an earlier release or a hand edit) does not block saves: it is kept as
  written, each save logs a WARN naming it, and it is skipped at runtime (also
  with a WARN) until renamed.

### Allowlisted tools

`policy.trust.allowlisted_tools` is not applied to the semantic view: an
allowlisted tool's output is still assessed (see
[Allowlisted Tools](#allowlisted-tools)).

### Blocked and terminated sessions after they end

When a session ends, the policy engine forgets its flagged entry, with one
exception: a session whose ladder action is `block` or `terminate` keeps a slim
entry (action, score, maximum severity, timestamps and content-free violations;
about 1.4 KB, measured). A client that reuses that session ID after the session ended still
meets the same action. At most 4096 ended sessions are retained, oldest evicted
first; an evicted ID no longer meets its old action. The full session record
has already been persisted at session end.

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

**Security note:** the OpenAI `user` field is client-controlled input — ELIDA trusts whatever value the caller puts in the request body. Session identity is not a cosmetic label: it's the key the kill-switch, risk ladder, and forensic capture all index by. In a single-tenant deployment — one trusted agent stack talking to its own ELIDA instance, ELIDA's common case — this is fine, and it's the whole point of the feature: the agent's own conversation ID keeps its session coherent across failover. In a multi-tenant, shared-key, or unauthenticated deployment, it's a liability: any client that knows or guesses another client's `user` value joins that client's session, and can then trigger a targeted session-kill, poison its risk score toward (or away from) enforcement, or pollute its forensic capture with noise. If clients are mutually untrusted, set `derive_from: {openai_user: false}` and either leave `body_path` empty or point it at a value the clients cannot guess or set for each other.

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

## Temporal correlation

`correlation` evaluates content-free policy event metadata (event IDs,
categories, source role and message index, timestamps) within each session.
Its history and session count are bounded, and the engine forgets a session
when that session ends.

**Restart-only, outside settings.** The block is top-level `correlation:` in
`elida.yaml` (not under `policy:`), and it is not part of `settings.yaml` or
`/control/settings`. A settings save or reset reloads the policy engine but
keeps the correlation engine and every session's correlation state; changing
the block needs a restart. `ELIDA_CORRELATION_ENABLED=true|false` overrides
`enabled`. Policy correlation (violation events and tool activity) reads the
policy engine's events, so it needs `policy.enabled: true`; shadow
correlation of semantic decisions does not (see
[semantic detection](semantic-detection.md#temporal-correlation)). With the
policy engine disabled, startup logs a WARN that policy correlation is
inactive while shadow correlation is active.

**Defaults.** Correlation is disabled by default. The shipped rule set is one
audit-only rule:

```yaml
correlation:
  enabled: false
  max_history_events: 64
  max_sessions: 4096
  rules:
    - name: slow_burn_injection
      shape: repeated
      category: injection_elevated
      count: 3
      count_by: message
      window: 15m
      cooldown: 15m
      severity: warning
      mode: audit
```

A `rules:` list in `elida.yaml` replaces the default list; it is not merged
(copy `slow_burn_injection` to keep it). `injection_elevated` violations are
recorded only when semantic detection runs in `audit` or `enforce`. In the
default `shadow` mode a separate shadow correlator, built from the same
rules and limits, correlates the elevated-band shadow decisions instead,
and records matches as `correlation_shadow` entries on the session
(persisted with the session record and served by
`GET /control/history/{id}`; see
[semantic detection](semantic-detection.md#temporal-correlation)), never as
violations.

**Reference.** Every key is restart-only.

| Key | Default | Notes |
|---|---|---|
| `correlation.enabled` | `false` | Env `ELIDA_CORRELATION_ENABLED=true\|false`. Policy correlation (violation events and tool activity) needs `policy.enabled: true`; shadow correlation of semantic decisions does not. |
| `correlation.max_history_events` | `64` | Events kept per session, shared by every category any rule wants; 1 to 1024, and at least the largest rule's `count` or sequence length. |
| `correlation.max_sessions` | `4096` | Sessions tracked, least recently active evicted first (per shard); 1 to 100000. |
| `correlation.rules` | `slow_burn_injection` | Replaces the default list. At most 64 rules. |
| `correlation.builtin_chains` | `false` | Adds `builtin_credential_exfiltration` and `builtin_sensitive_exfiltration` (audit-only sequences, 15m) and the `credential_discovery`, `sensitive_access`, `external_transmission` tool categories listed under [Built-in chains](#built-in-chains). A rule with the same name replaces a built-in. |
| `correlation.tool_categories` | none | Tool-call taxonomy: entries mapping tool name globs and optional argument patterns to a category. At most 64 entries. |
| `correlation.max_argument_bytes` | `16384` | How much of a tool call's arguments argument patterns scan; at most 65536. |

Rule fields:

| Field | Applies to | Notes |
|---|---|---|
| `name` | all | `[a-z0-9_]`, at most 64 bytes, unique; recorded as `correlation.<name>`. |
| `shape` | all | `repeated` or `sequence`. |
| `category` | repeated | The category counted. |
| `count` | repeated | 2 to 32. |
| `count_by` | repeated | `event` (default) or `message` (source role and message index). |
| `sequence` | sequence | 2 to 8 categories, in order by event time. |
| `window` | all | First to last event, inclusive; at most 24h. |
| `cooldown` | all | Default: `window`; at most 24h. |
| `severity` | all | `info`, `warning` (default) or `critical`. |
| `mode` | all | `audit` (default) or `enforce` (reserved decision; startup WARN; capped by `policy.mode: audit`). |

Categories available to rules are the policy event categories (for example
`injection_elevated`, `semantic_injection`, `prompt_injection`,
rule-derived content categories) and the categories defined in
`tool_categories`. `correlation_match` is reserved.

`tool_categories` entry fields and limits:

| Field | Notes |
|---|---|
| `category` | Category name, same charset as rule categories. |
| `tools` | 1 to 32 case-insensitive globs over the whole tool name (`*` and `?` match any character, `/` included; at most 128 bytes each). |
| `argument_patterns` | Optional, at most 8 case-insensitive RE2 regexes (at most 1024 bytes each). Without patterns, the tool name alone categorizes the call. |

Every glob and pattern is compiled at load. One is rejected above 256
compiled instructions, and the whole taxonomy above 8192. Over 1024 summed
instructions the taxonomy is accepted with a startup WARN that estimates
its worst-case cost per call. See the
[cost model](semantic-detection.md#temporal-correlation).

```yaml
correlation:
  enabled: true
  rules:
    - name: slow_burn_injection
      shape: repeated
      category: injection_elevated
      count: 3
      count_by: message
      window: 15m
    - name: cred_then_send
      shape: sequence
      sequence: [credential_discovery, external_transmission]
      window: 10m
  max_argument_bytes: 16384
  tool_categories:
    - category: credential_discovery
      tools: [read_file, bash]
      argument_patterns: ['\.aws/credentials', 'id_rsa']
    - category: external_transmission
      tools: ['http_*', web_fetch]
```

### Built-in chains

`correlation.builtin_chains: true` adds a shipped, audit-only preset: two
sequence rules and the tool categories they need. It is off by default and
it is a heuristic. Tool names and argument shapes vary by agent framework,
so the globs and patterns are listed here in full for operators to judge.
Nothing about them is hidden in the binary.

Rules (both `shape: sequence`, `window: 15m`, `severity: warning`,
`mode: audit`):

| Rule | Sequence |
|---|---|
| `builtin_credential_exfiltration` | `credential_discovery`, `external_transmission` |
| `builtin_sensitive_exfiltration` | `sensitive_access`, `external_transmission` |

Tool categories (globs are case-insensitive over the whole tool name;
patterns are case-insensitive RE2 over the arguments):

| Category | Tools | Argument patterns |
|---|---|---|
| `credential_discovery` | `*` | `\.aws/credentials`, `\.ssh/id_[a-z0-9]+`, `\.netrc\b`, `\.git-credentials`, `\.kube/config`, `\.docker/config\.json`, `(^\|[/\\"' ])\.env\b` |
| `sensitive_access` | `*` | `/etc/shadow`, `/etc/sudoers`, `/proc/self/environ`, `\.(bash\|zsh)_history` |
| `external_transmission` | `*fetch*`, `*http*`, `*request*`, `*upload*`, `*webhook*`, `send_*`, `post_*` | none (the name alone categorizes the call) |
| `external_transmission` | `bash`, `sh`, `shell`, `terminal`, `run_command`, `*exec*` | `\b(curl\|wget)\b[^\n]*https?://`, `\bnc\b\s+\S+\s+\d+`, `\bscp\b\s+\S+\s+\S+@` |

Merge rule: the built-in taxonomy entries are appended after the configured
`tool_categories` (a category may repeat; a call still gets it once), and a
built-in rule is appended unless a configured rule already has its name.
Redefining `builtin_credential_exfiltration` under `rules:` therefore
replaces it, for example with another window or, as a reserved decision,
`mode: enforce`. Validation and the compiled-instruction caps apply to the
merged set, which is also what the startup summary describes
(`builtin_chains=true`). The preset compiles to well under the 1024
instruction soft limit, so it adds no startup warning. Matches are recorded
as evidence-only `correlation.builtin_credential_exfiltration` and
`correlation.builtin_sensitive_exfiltration` violations and add no risk.

**Validation.** Rule names, categories, counts, windows and memory limits are
validated when the file is loaded, exactly as the engine will be built (an
explicit `max_history_events: 0` is an error, not "use the default"). An
enabled block that cannot be built refuses startup.

**Shapes.** `shape: repeated` matches `count` events of one `category`
within `window`. `shape: sequence` matches 2 to 8 categories listed under
`sequence:`, in that order by event time, with the first and last no more
than `window` apart (inclusive); it takes no `category`, `count` or
`count_by`:

```yaml
    - name: probe_then_exfil
      shape: sequence
      sequence: [injection_elevated, policy_violation]
      window: 10m
```

Order is decided by event timestamps, not by arrival, so an asynchronous
semantic result that lands after later events still fills its step. Events
with equal timestamps follow arrival order. A step may repeat a category
(`[a, a, b]` needs two `a` events before the `b`). For both shapes, each
event contributes to at most one match of a rule (it may also contribute to
other rules), and `cooldown` (default: `window`) suppresses a further match
of the rule until that long after the previous match's last event.

**Modes.** The default `mode` is `audit`: a match is evidence-only, adds no
risk, and is exported as an informational OCSF 2004 Detection Finding with
`evidence_only`. `mode: enforce` makes a match contribute the rule's severity
at full weight to the existing risk ladder. Promoting a rule to enforce is a
reserved decision; startup logs a WARN for each enforce rule, and
`policy.mode: audit` caps enforce rules to evidence-only.

**Records.** A match is recorded as a violation named `correlation.<rule>`
with event category `correlation_match`, its match ID as the event ID, and a
`correlation` object (match ID, rule, shape, mode, contributing event IDs and
categories, first and last event time) on `GET /control/flagged/{id}`. The
`correlation.` prefix is reserved: neither `elida.yaml` nor a settings save
may name a rule with it. `GET /control/flagged/stats` reports a
`correlation` object (events queued and observed, matches, drops, shard
count) while correlation is enabled.

`count_by: message` counts distinct messages (source role and message
index; index 0 is a real message, so several windows of one long first
message count once). Events with no message at all (metric and anomaly
violations, which carry no source role) count individually.
