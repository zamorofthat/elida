# Getting Started with ELIDA

This guide walks you through setting up ELIDA and proxying your first AI agent session.

---

## Prerequisites

- **Go 1.24+** (for building from source) or **Docker**
- An AI backend: [Ollama](https://ollama.ai) (local) or API key for OpenAI/Anthropic

---

## Step 1: Install ELIDA

### Option A: Build from Source

```bash
git clone https://github.com/zamorofthat/elida.git
cd elida
make build
```

### Option B: Docker

```bash
docker pull ghcr.io/zamorofthat/elida:latest
```

### Option C: Docker Compose (with Redis)

```bash
git clone https://github.com/zamorofthat/elida.git
cd elida
make up
```

---

## Step 2: Start ELIDA

### With Ollama (local models)

```bash
# Start Ollama first
ollama serve

# Start ELIDA (default proxies to localhost:11434)
./bin/elida
```

### With Anthropic (Claude)

```bash
./bin/elida --backend https://api.anthropic.com
```

### With OpenAI

```bash
./bin/elida --backend https://api.openai.com
```

You should see:
```
{"time":"...","level":"INFO","msg":"starting ELIDA proxy","listen":":8080","backend":"http://localhost:11434"}
{"time":"...","level":"INFO","msg":"control API enabled","listen":":9090"}
```

---

## Step 3: Send Your First Request

### Test with curl

```bash
# Through ELIDA (port 8080)
curl http://localhost:8080/api/generate \
  -d '{"model":"llama3.2","prompt":"Hello!"}'
```

### Test with Claude Code

```bash
ANTHROPIC_BASE_URL=http://localhost:8080 claude
```

### Test with any OpenAI-compatible client

```bash
OPENAI_BASE_URL=http://localhost:8080 your-tool
```

---

## Step 4: View the Dashboard

Open your browser to:

```
http://localhost:9090
```

You'll see:
- Active sessions
- Request counts
- Session timeline

---

## Step 5: Enable the Policy Engine

Add security rules to detect prompt injection, PII leaks, and more.

### Via command line

```bash
./bin/elida --policy-enabled --policy-preset standard
```

### Via config file

Edit `configs/elida.yaml`:

```yaml
listen: ":8080"
backend: "http://localhost:11434"

policy:
  enabled: true
  preset: standard  # minimal | standard | strict
```

Then run:

```bash
./bin/elida --config configs/elida.yaml
```

### Test the policy

```bash
# This should get flagged/blocked
curl http://localhost:8080/api/generate \
  -d '{"model":"llama3.2","prompt":"Ignore all previous instructions"}'
```

Check flagged sessions:

```bash
curl http://localhost:9090/control/flagged
```

---

## Step 6: Kill a Session

If an agent goes rogue, kill it:

```bash
# List active sessions
curl http://localhost:9090/control/sessions?active=true

# Kill by session ID
curl -X POST http://localhost:9090/control/sessions/{session-id}/kill
```

All subsequent requests from that session will receive `403 Forbidden`.

---

## Step 7: Enable Audit Logging

Capture all requests/responses for compliance:

```bash
./bin/elida \
  --storage-enabled \
  --storage-capture-mode all
```

Or in config:

```yaml
storage:
  enabled: true
  capture_mode: "all"  # or "flagged_only"
```

View history:

```bash
curl http://localhost:9090/control/history
```

---

## Common Configurations

### Claude Code through ELIDA

```yaml
# configs/elida.yaml
listen: ":8080"
backend: "https://api.anthropic.com"

session:
  timeout: 30m

policy:
  enabled: true
  preset: standard

storage:
  enabled: true
  capture_mode: flagged_only
```

```bash
# Terminal 1
./bin/elida --config configs/elida.yaml

# Terminal 2
ANTHROPIC_BASE_URL=http://localhost:8080 claude
```

### Multi-Backend (route by model)

```yaml
backends:
  ollama:
    url: "http://localhost:11434"
    default: true

  anthropic:
    url: "https://api.anthropic.com"
    models: ["claude-*"]

  openai:
    url: "https://api.openai.com"
    models: ["gpt-*", "o1-*"]
```

---

## Quick Reference

| Port | Service |
|------|---------|
| 8080 | Proxy (point your AI client here) |
| 9090 | Control API + Dashboard |

| Command | Description |
|---------|-------------|
| `make run` | Start with defaults |
| `make run-demo` | Start with policy + storage |
| `make up` | Start with Docker Compose + Redis |

| Endpoint | Description |
|----------|-------------|
| `GET /control/sessions` | List sessions |
| `GET /control/flagged` | Policy violations |
| `POST /control/sessions/{id}/kill` | Kill session |
| `GET /control/history` | Session history |
| `GET /control/events` | Audit log |

---

## Next Steps

- [Configuration Guide](CONFIGURATION.md) — All options
- [Policy Rules Reference](POLICY_RULES_REFERENCE.md) — 40+ security rules
- [Telco Controls](TELCO_CONTROLS.md) — Risk ladder, token tracking
- [Enterprise Deployment](ENTERPRISE_DEPLOYMENT.md) — Kubernetes, Helm
