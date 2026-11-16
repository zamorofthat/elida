# ELIDA Deployment Guide

## Quick Start

### Docker (Simplest)

```bash
docker run -d \
  -p 8080:8080 \
  -p 127.0.0.1:9090:9090 \
  -e ELIDA_BACKEND=https://api.anthropic.com \
  zamorofthat/elida:latest
```

### Docker Compose (with Redis)

```bash
# Clone the repo
git clone https://github.com/zamorofthat/elida.git
cd elida

# Start full stack
docker-compose up -d

# View logs
docker-compose logs -f elida
```

### Binary (Direct Install)

```bash
# Download latest release
curl -LO https://github.com/zamorofthat/elida/releases/latest/download/elida-linux-amd64
chmod +x elida-linux-amd64
mv elida-linux-amd64 /usr/local/bin/elida

# Run
elida -config /etc/elida/elida.yaml
```

---

## AI Client Configuration

### Claude Code

```bash
# Option 1: Per-session environment variable
ANTHROPIC_BASE_URL=http://localhost:8080 claude

# Option 2: Permanent export (add to ~/.zshrc or ~/.bashrc)
export ANTHROPIC_BASE_URL=http://localhost:8080
```

**Option 3: Settings file (recommended)**

Create or edit `~/.claude/settings.json`:
```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://localhost:8080"
  }
}
```

See [Claude Code LLM Gateway docs](https://code.claude.com/docs/en/llm-gateway) for more options.

### Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "apiBaseUrl": "http://localhost:8080"
}
```

### OpenAI SDK / ChatGPT CLI

```bash
export OPENAI_BASE_URL=http://localhost:8080
```

### Cursor IDE

Settings → Models → Override OpenAI Base URL → `http://localhost:8080`

### Continue.dev

Edit `~/.continue/config.yaml`:

```yaml
models:
  - name: claude-3-5-sonnet
    provider: anthropic
    apiBase: http://localhost:8080
```

---

## Production Deployment

### Kubernetes (Helm)

```bash
# Add values
cat > values.yaml << EOF
config:
  backend: "https://api.anthropic.com"
  policy:
    enabled: true
    preset: standard
redis:
  enabled: true
EOF

# Install
helm install elida ./deploy/helm/elida -f values.yaml
```

### AWS ECS

See `deploy/ecs/` for CloudFormation templates and task definitions.

### Terraform

See `deploy/terraform/` for AWS infrastructure modules.

### Stopping ELIDA

On `SIGTERM` or `SIGINT` ELIDA stops in two phases:

1. It stops accepting connections and waits up to `shutdown_timeout`
   (default `30s`, env `ELIDA_SHUTDOWN_TIMEOUT`) for in-flight requests to
   finish.
2. It ends every live session: each one is saved to session history (when
   storage is enabled) and exported (OpenTelemetry, OCSF). This phase is
   **not** bounded by `shutdown_timeout`; it takes as long as the sessions
   take to save.

A live session is held in memory until it ends or is flagged. If the
container runtime kills ELIDA before phase 2 finishes, the sessions it had not
reached are missing from history and telemetry. So the runtime must allow more
time than `shutdown_timeout`, and most defaults do not:

| Runtime | Setting | Runtime default | Shipped here |
|---------|---------|-----------------|--------------|
| `docker run` / `docker stop` | `--stop-timeout 60` / `docker stop -t 60` | 10s | set it yourself |
| Docker Compose | `stop_grace_period` | 10s | `60s` in `docker-compose.yaml` and `deploy/docker-compose.prod.yaml` |
| Kubernetes | `terminationGracePeriodSeconds` | 30s | `60` in the Helm chart (`terminationGracePeriodSeconds`) |

60 seconds is `shutdown_timeout` plus 30 seconds for phase 2. If you raise
`shutdown_timeout`, raise the grace period by the same amount.

How long phase 2 takes depends on the capture mode. Measured on v0.32.1
(Apple M1 Max, memory session store, SQLite on a local SSD, redaction on):

- `capture_mode: flagged_only` (default): about 0.5 ms per live session.
  5,000 live sessions were saved in 2.5 s.
- `capture_mode: all`: each session's captured bodies are redacted when the
  session is saved, one session at a time on one core, at about 0.4 ms per KB
  of captured content (roughly 2.6 MB/s). 1,000 live sessions holding 50 KB
  each took 19 s; 100 sessions holding 1 MB each took 37 s. A session can
  hold up to `max_captured_per_session` x 2 x `max_capture_size` (2 MB with
  the defaults).

Treat these as an order of magnitude: a CPU limit below one core (the chart's
default is `500m`) or a slower disk makes it proportionally slower. To size
your own deployment, compare the timestamps of the `shutting down gracefully`
and `ELIDA stopped` log lines on a normal stop. With `capture_mode: all` and
more than a few tens of MB of live captured content, raise the grace period
to match.

---

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `ELIDA_LISTEN` | Proxy listen address | `:8080` |
| `ELIDA_BACKEND` | Default backend URL | `http://localhost:11434` |
| `ELIDA_CONTROL_LISTEN` | Control API address inside the container | `:9090` |
| `ELIDA_POLICY_ENABLED` | Enable security policies | `false` |
| `ELIDA_POLICY_PRESET` | Preset: `minimal`, `standard`, `strict` | - |
| `ELIDA_STORAGE_ENABLED` | Enable session history (SQLite) | `false` |
| `ELIDA_STORAGE_CAPTURE_MODE` | Capture mode: `flagged_only` or `all` | `flagged_only` |
| `ELIDA_SESSION_STORE` | Store: `memory` or `redis` | `memory` |
| `ELIDA_REDIS_ADDR` | Redis address | `localhost:6379` |
| `ELIDA_TLS_ENABLED` | Enable HTTPS | `false` |
| `ELIDA_TELEMETRY_ENABLED` | Enable OpenTelemetry export | `false` |
| `ELIDA_TELEMETRY_EXPORTER` | Exporter: `otlp`, `stdout`, `none` | `none` |
| `ELIDA_TELEMETRY_ENDPOINT` | OTLP collector endpoint | `localhost:4317` |

### Config File

```yaml
listen: ":8080"

backends:
  anthropic:
    url: "https://api.anthropic.com"
    api_key: "${ANTHROPIC_API_KEY}"   # or omit: auto-read from ANTHROPIC_API_KEY
    models: ["claude-*"]
    default: true
  openai:
    url: "https://api.openai.com"
    api_key: "${OPENAI_API_KEY}"      # or omit: auto-read from OPENAI_API_KEY
    models: ["gpt-*", "o1-*"]

# Failover across the backends above when one fails (disabled by default).
# See docs/configuration.md#failover for the full model-rewrite behavior.
failover:
  enabled: true
  fallback_order: [anthropic, openai]   # set explicitly — see recommendation in configuration.md

session:
  timeout: 30m
  header: "X-Session-ID"

policy:
  enabled: true
  preset: standard
  mode: enforce

control:
  enabled: true
  listen: ":9090"
  auth:
    enabled: true
    api_key: "${ELIDA_CONTROL_API_KEY}"

storage:
  enabled: true
  path: "/data/elida.db"
```

---

## Observability

### OpenTelemetry Export

ELIDA can export session records to any OTLP-compatible collector (Jaeger, Grafana Tempo, Datadog, etc.). **Telemetry works independently of SQLite storage** — you can export to OTEL without enabling local persistence.

```bash
docker run -d \
  -e ELIDA_TELEMETRY_ENABLED=true \
  -e ELIDA_TELEMETRY_EXPORTER=otlp \
  -e ELIDA_TELEMETRY_ENDPOINT=otel-collector:4317 \
  -e ELIDA_POLICY_ENABLED=true \
  -e ELIDA_POLICY_PRESET=standard \
  zamorofthat/elida:latest
```

Session records exported include:
- Session ID, state, duration, request count
- Bytes in/out
- Policy violations (if any)
- Captured request/response content (if policy flagged)

### Full Audit Trail (SQLite + OTEL)

For compliance or debugging, enable both:

```bash
docker run -d \
  --stop-timeout 60 \
  -v elida-data:/data \
  -e ELIDA_STORAGE_ENABLED=true \
  -e ELIDA_STORAGE_CAPTURE_MODE=all \
  -e ELIDA_TELEMETRY_ENABLED=true \
  -e ELIDA_TELEMETRY_EXPORTER=otlp \
  -e ELIDA_TELEMETRY_ENDPOINT=otel-collector:4317 \
  -e ELIDA_POLICY_ENABLED=true \
  -e ELIDA_POLICY_PRESET=standard \
  zamorofthat/elida:latest
```

`--stop-timeout 60` gives ELIDA time to save its live sessions on
`docker stop`; see [Stopping ELIDA](#stopping-elida).

---

## Security Checklist

- [ ] Enable TLS in production (`ELIDA_TLS_ENABLED=true`)
- [ ] Enable control API authentication (`ELIDA_CONTROL_API_KEY=...`)
- [ ] Use `strict` policy preset for high-security environments
- [ ] Restrict control port (9090) to internal network
- [ ] Enable Redis for horizontal scaling
- [ ] Set up log aggregation for audit trail
- [ ] Enable telemetry export for centralized observability (`ELIDA_TELEMETRY_ENABLED=true`)

---

## Verifying Installation

```bash
# Health check
curl http://localhost:9090/control/health

# View active sessions
curl http://localhost:9090/control/sessions

# View dashboard
open http://localhost:9090/
```

---

## Troubleshooting

### Connection refused on port 8080
- Check if ELIDA is running: `docker ps` or `ps aux | grep elida`
- Check logs: `docker logs elida` or `journalctl -u elida`

### Sessions not appearing
- Verify client is using ELIDA as base URL
- Check `X-Session-ID` header in responses

### Policy blocking requests unexpectedly
- Switch to audit mode: `ELIDA_POLICY_MODE=audit`
- Check flagged sessions: `curl http://localhost:9090/control/flagged`

---

## Links

- [GitHub](https://github.com/zamorofthat/elida)
- [Docker Hub](https://hub.docker.com/r/zamorofthat/elida)
- [Architecture](architecture.md)
- [Security Policy](security.md)
