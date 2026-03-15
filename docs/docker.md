# ELIDA - Edge Layer for Intelligent Defense of Agents

Session-aware reverse proxy for securing AI agent traffic. Think of it as a Session Border Controller (SBC) for AI.

## Features

- **Session Tracking** - Track agent sessions by client IP + backend
- **Kill Switch** - Instantly terminate runaway or compromised agents
- **Policy Engine** - OWASP LLM Top 10 security rules built-in
- **Multi-Backend** - Route to OpenAI, Anthropic, Mistral, Ollama, etc.
- **WebSocket Support** - Real-time voice agents (OpenAI Realtime, Deepgram)
- **Audit Logging** - Full request/response capture for compliance
- **Dashboard** - Built-in web UI for monitoring sessions

## Quick Start

```bash
docker run -d \
  -p 8080:8080 \
  -p 9090:9090 \
  -e ELIDA_BACKEND=https://api.openai.com \
  zamorofthat/elida:latest
```

Then configure your AI tools to use `http://localhost:8080` as the API base URL.

## With Policy Engine

```bash
docker run -d \
  -p 8080:8080 \
  -p 9090:9090 \
  -e ELIDA_BACKEND=https://api.openai.com \
  -e ELIDA_POLICY_ENABLED=true \
  -e ELIDA_POLICY_PRESET=standard \
  zamorofthat/elida:latest
```

## Full Audit Trail (Policy + Storage)

Capture all requests/responses for compliance:

```bash
docker run -d \
  -p 8080:8080 \
  -p 9090:9090 \
  -v elida-data:/data \
  -e ELIDA_BACKEND=https://api.anthropic.com \
  -e ELIDA_POLICY_ENABLED=true \
  -e ELIDA_POLICY_PRESET=standard \
  -e ELIDA_STORAGE_ENABLED=true \
  -e ELIDA_STORAGE_CAPTURE_MODE=all \
  zamorofthat/elida:latest
```

## With OpenTelemetry Export

Export session records to Jaeger, Datadog, etc.:

```bash
docker run -d \
  -p 8080:8080 \
  -p 9090:9090 \
  -e ELIDA_BACKEND=https://api.anthropic.com \
  -e ELIDA_POLICY_ENABLED=true \
  -e ELIDA_POLICY_PRESET=standard \
  -e ELIDA_TELEMETRY_ENABLED=true \
  -e ELIDA_TELEMETRY_EXPORTER=otlp \
  -e ELIDA_TELEMETRY_ENDPOINT=otel-collector:4317 \
  zamorofthat/elida:latest
```

## With Redis (Horizontal Scaling)

Share session state across multiple instances:

```bash
docker run -d \
  -p 8080:8080 \
  -p 9090:9090 \
  -e ELIDA_BACKEND=https://api.anthropic.com \
  -e ELIDA_SESSION_STORE=redis \
  -e ELIDA_REDIS_ADDR=redis:6379 \
  -e ELIDA_POLICY_ENABLED=true \
  -e ELIDA_POLICY_PRESET=standard \
  zamorofthat/elida:latest
```

## Strict Mode for Autonomous Agents

Maximum protection for unattended agents:

```bash
# Generate a random API key
export ELIDA_CONTROL_API_KEY=$(openssl rand -base64 32)

docker run -d \
  -p 8080:8080 \
  -p 9090:9090 \
  -v elida-data:/data \
  -e ELIDA_BACKEND=https://api.anthropic.com \
  -e ELIDA_POLICY_ENABLED=true \
  -e ELIDA_POLICY_PRESET=strict \
  -e ELIDA_STORAGE_ENABLED=true \
  -e ELIDA_STORAGE_CAPTURE_MODE=all \
  -e ELIDA_SESSION_TIMEOUT=30m \
  -e ELIDA_CONTROL_API_KEY=$ELIDA_CONTROL_API_KEY \
  zamorofthat/elida:latest

# Access control API with auth
curl -H "Authorization: Bearer $ELIDA_CONTROL_API_KEY" \
  http://localhost:9090/control/sessions
```

## Control API Authentication

**Recommended for production.** The control API can kill sessions and view captured content - protect it.

```bash
# Generate your own API key (save this somewhere secure)
export ELIDA_CONTROL_API_KEY=$(openssl rand -base64 32)
echo "Save this key: $ELIDA_CONTROL_API_KEY"

# Run with auth enabled (setting the key auto-enables auth)
docker run -d \
  -p 8080:8080 \
  -p 9090:9090 \
  -e ELIDA_BACKEND=https://api.anthropic.com \
  -e ELIDA_CONTROL_API_KEY=$ELIDA_CONTROL_API_KEY \
  zamorofthat/elida:latest
```

Access the control API with Bearer token:
```bash
# Without auth: 401 Unauthorized
curl http://localhost:9090/control/sessions

# With auth: 200 OK
curl -H "Authorization: Bearer $ELIDA_CONTROL_API_KEY" \
  http://localhost:9090/control/sessions

# Kill a session
curl -X POST -H "Authorization: Bearer $ELIDA_CONTROL_API_KEY" \
  http://localhost:9090/control/sessions/{session_id}/kill
```

**Security best practices:**
- **Don't expose port 9090 publicly** - Bind to localhost only (see below)
- **Use Docker secrets** instead of env vars for sensitive keys in production
- **Enable TLS** for the control API if accessed over a network
- **Rotate keys** periodically and after any suspected compromise

```bash
# Production: control API on localhost only, proxy on all interfaces
docker run -d \
  -p 8080:8080 \
  -p 127.0.0.1:9090:9090 \
  -e ELIDA_BACKEND=https://api.anthropic.com \
  -e ELIDA_CONTROL_API_KEY=$ELIDA_CONTROL_API_KEY \
  zamorofthat/elida:latest
```

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `ELIDA_LISTEN` | Proxy listen address | `:8080` |
| `ELIDA_BACKEND` | Default backend URL | `http://localhost:11434` |
| `ELIDA_CONTROL_LISTEN` | Control API address | `:9090` |
| `ELIDA_CONTROL_AUTH_ENABLED` | Enable control API auth | `false` |
| `ELIDA_CONTROL_API_KEY` | API key for control API | - |
| `ELIDA_POLICY_ENABLED` | Enable security policies | `false` |
| `ELIDA_POLICY_PRESET` | Preset: `minimal`, `standard`, `strict` | `standard` |
| `ELIDA_POLICY_MODE` | Mode: `enforce` or `audit` | `enforce` |
| `ELIDA_STORAGE_ENABLED` | Enable session history (SQLite) | `false` |
| `ELIDA_STORAGE_CAPTURE_MODE` | Capture mode: `flagged_only` or `all` | `flagged_only` |
| `ELIDA_SESSION_STORE` | Session store: `memory` or `redis` | `memory` |
| `ELIDA_SESSION_TIMEOUT` | Session idle timeout | `5m` |
| `ELIDA_REDIS_ADDR` | Redis address for scaling | `localhost:6379` |
| `ELIDA_TELEMETRY_ENABLED` | Enable OpenTelemetry export | `false` |
| `ELIDA_TELEMETRY_EXPORTER` | Exporter: `otlp`, `stdout` | `none` |
| `ELIDA_TELEMETRY_ENDPOINT` | OTLP collector endpoint | `localhost:4317` |

## Endpoints

| Port | Purpose |
|------|---------|
| `8080` | Proxy - point your AI tools here |
| `9090` | Control API & Dashboard |

## Control API

```bash
# Health check
curl http://localhost:9090/control/health

# List active sessions
curl http://localhost:9090/control/sessions

# Kill a session
curl -X POST http://localhost:9090/control/sessions/{id}/kill

# View dashboard
open http://localhost:9090/
```

## Multi-Backend Configuration

Mount a config file for advanced routing:

```bash
docker run -d \
  -p 8080:8080 \
  -p 9090:9090 \
  -v $(pwd)/elida.yaml:/etc/elida/elida.yaml \
  zamorofthat/elida:latest \
  -config /etc/elida/elida.yaml
```

Example `elida.yaml`:
```yaml
listen: ":8080"
backends:
  openai:
    url: "https://api.openai.com"
    models: ["gpt-*", "o1-*"]
    default: true
  anthropic:
    url: "https://api.anthropic.com"
    models: ["claude-*"]
  mistral:
    url: "https://api.mistral.ai"
    models: ["mistral-*"]
control:
  enabled: true
  listen: ":9090"
policy:
  enabled: true
  preset: standard
```

## Kubernetes / Helm

```bash
helm install elida ./deploy/helm/elida
```

## Links

- **GitHub**: https://github.com/zamorofthat/elida
- **Documentation**: https://github.com/zamorofthat/elida#readme
- **Helm Chart**: https://github.com/zamorofthat/elida/tree/main/deploy/helm

## Tags

- `latest` - Latest stable release
- `vX.Y.Z` - Specific version
- `main` - Latest from main branch 
