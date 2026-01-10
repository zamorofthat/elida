# ELIDA

**Edge Layer for Intelligent Defense of Agents**

ELIDA is a session-aware proxy for AI agents. It provides visibility, control, and security for agent-to-agent and agent-to-LLM communication.

## Why ELIDA?

As enterprises deploy AI agents, security teams need:
- **Visibility** — See what agents are doing in real-time
- **Control** — Kill runaway sessions, enforce timeouts
- **Audit** — Complete session logs for compliance
- **Protection** — Rate limiting, policy enforcement (roadmap)

Think of it like a Session Border Controller (SBC) from telecom, but for AI agents.

## Features

### MVP (Current)
- [x] HTTP reverse proxy with request/response capture
- [x] Streaming support (NDJSON for Ollama, SSE for OpenAI/Anthropic/Mistral)
- [x] Session tracking and management
- [x] Session timeout enforcement
- [x] Kill switch for active sessions
- [x] Control API for monitoring
- [x] Structured JSON logging

### Roadmap
- [ ] WebSocket support for real-time/voice agents
- [ ] Policy engine for session-level rules
- [ ] Content inspection and PII detection
- [ ] OpenTelemetry integration
- [ ] Dashboard UI
- [ ] Redis-backed session store
- [ ] SDK for native agent integration

## Quick Start

### Prerequisites
- Go 1.22+
- An LLM backend (Ollama, OpenAI API, etc.)

### Build and Run

```bash
# Clone the repo
git clone https://github.com/yourusername/elida.git
cd elida

# Build
make build

# Run (defaults to proxying localhost:11434)
make run
```

### Configuration

Edit `configs/elida.yaml`:

```yaml
listen: ":8080"
backend: "http://localhost:11434"  # Your LLM backend

session:
  timeout: 5m
  header: "X-Session-ID"
  generate_if_missing: true

control:
  listen: ":9090"
  enabled: true
```

Or use environment variables:
```bash
export ELIDA_BACKEND="https://api.mistral.ai"
export ELIDA_LISTEN=":8080"
```

## Usage

### Proxy Traffic

Point your agent at ELIDA instead of the LLM directly:

```bash
# Before: direct to Ollama
curl http://localhost:11434/api/generate -d '{"model":"qwen:0.5b","prompt":"Hello"}'

# After: through ELIDA
curl http://localhost:8080/api/generate -d '{"model":"qwen:0.5b","prompt":"Hello"}'
```

ELIDA will:
1. Create/resume a session
2. Forward the request to the backend
3. Capture the response (including streaming)
4. Log everything
5. Return the response with `X-Session-ID` header

### Control API

```bash
# Health check
curl http://localhost:9090/control/health

# Get stats
curl http://localhost:9090/control/stats

# List active sessions
curl http://localhost:9090/control/sessions?active=true

# Get session details
curl http://localhost:9090/control/sessions/{session-id}

# Kill a session
curl -X POST http://localhost:9090/control/sessions/{session-id}/kill
```

### Session Management

Sessions are identified by the `X-Session-ID` header. If not provided, ELIDA generates one.

```bash
# Use explicit session ID
curl -H "X-Session-ID: my-agent-task-123" http://localhost:8080/api/generate ...

# Response includes the session ID
< X-Session-ID: my-agent-task-123
```

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                        ELIDA                            │
├──────────────┬──────────────┬───────────────────────────┤
│    Proxy     │   Session    │     Control API           │
│   Handler    │   Manager    │   GET  /sessions          │
│              │              │   POST /sessions/{id}/kill│
│  HTTP/NDJSON │  Lifecycle   │   GET  /stats             │
│  SSE         │  Timeouts    │   GET  /health            │
│  (WebSocket) │  Cleanup     │                           │
├──────────────┴──────────────┴───────────────────────────┤
│              Session Store (In-Memory / Redis)          │
└─────────────────────────────────────────────────────────┘
           │                              │
           ▼                              ▼
    ┌─────────────┐               ┌─────────────┐
    │   Agents    │               │   Backend   │
    │  (Clients)  │               │   (LLMs)    │
    └─────────────┘               └─────────────┘
```

## Development

```bash
# Run tests
make test

# Format code
make fmt

# Build Docker image
make docker
```

## License

MIT

## Why "ELIDA"?

**E**dge **L**ayer for **I**ntelligent **D**efense of **A**gents

Also named after the developer's grandmother.
