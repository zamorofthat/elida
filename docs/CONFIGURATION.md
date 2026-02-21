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
  anthropic:
    url: "https://api.anthropic.com"
    type: anthropic
    models: ["claude-*"]
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
  endpoint: ""
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

## Session ID Behavior

Sessions are identified by the `X-Session-ID` header. If not provided, ELIDA generates one automatically.

```bash
# Use explicit session ID
curl -H "X-Session-ID: my-agent-task-123" http://localhost:8080/api/generate ...

# Response includes the session ID
< X-Session-ID: my-agent-task-123
```

For Claude Code, ELIDA uses client IP-based session tracking, so all requests from the same IP are grouped into a single session automatically.
