# ELIDA Deployment Guide

## Quick Start

### Docker (Simplest)

```bash
docker run -d \
  -p 8080:8080 \
  -p 9090:9090 \
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

---

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `ELIDA_LISTEN` | Proxy listen address | `:8080` |
| `ELIDA_BACKEND` | Default backend URL | `http://localhost:11434` |
| `ELIDA_CONTROL_LISTEN` | Control API address | `:9090` |
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
    models: ["claude-*"]
    default: true
  openai:
    url: "https://api.openai.com"
    models: ["gpt-*", "o1-*"]

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
- [Architecture](ARCHITECTURE.md)
- [Security Policy](SECURITY.md)
