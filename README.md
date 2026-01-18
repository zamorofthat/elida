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

### Current Features
- [x] HTTP reverse proxy with request/response capture
- [x] Streaming support (NDJSON for Ollama, SSE for OpenAI/Anthropic/Mistral)
- [x] Session tracking and management
- [x] Session timeout enforcement
- [x] Kill/Resume/Terminate session lifecycle
- [x] Control API for monitoring
- [x] Structured JSON logging
- [x] Redis-backed session store for horizontal scaling
- [x] OpenTelemetry integration for tracing
- [x] SQLite storage for session history
- [x] Dashboard UI for monitoring
- [x] Client IP-based session tracking (for Claude Code)
- [x] Multi-backend routing (route by header, model name, or path)
- [x] TLS/HTTPS support

### Security Features
- [x] Policy engine with 40+ built-in rules (OWASP LLM Top 10)
- [x] Content inspection for requests and responses
- [x] Prompt injection detection and blocking (LLM01)
- [x] Output security scanning - XSS, SQL, shell patterns (LLM02)
- [x] PII and credential detection (LLM06)
- [x] Tool/plugin security monitoring (LLM07)
- [x] Excessive agency prevention (LLM08)
- [x] Model theft detection (LLM10)
- [x] Audit mode for dry-run evaluation
- [x] Chunked streaming scan (low latency) and buffered mode (full validation)

### Roadmap
- [ ] WebSocket support for real-time/voice agents
- [ ] LLM-as-judge content moderation (local Gemma models)
- [ ] Validation webhook for model output QA
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

  # Kill block configuration - how long killed sessions stay blocked
  kill_block:
    # Mode options:
    #   "duration"          - Block for a specific duration after kill
    #   "until_hour_change" - Block until the hour changes (session ID regenerates)
    #   "permanent"         - Block permanently until server restart
    mode: "duration"
    duration: 30m

control:
  listen: ":9090"
  enabled: true

# Policy engine for flagging suspicious sessions
policy:
  enabled: true
  capture_flagged: true
  rules:
    - name: "high_request_count"
      type: "request_count"
      threshold: 100
      severity: "warning"
```

Or use environment variables:
```bash
export ELIDA_BACKEND="https://api.mistral.ai"
export ELIDA_LISTEN=":8080"
```

### Using with Claude Code

ELIDA can proxy Claude Code traffic to monitor and control agent sessions:

```bash
# Configure ELIDA to proxy to Anthropic
# In configs/elida.yaml:
backend: "https://api.anthropic.com"

# Start ELIDA
make run

# Start Claude Code with ELIDA as the base URL
ANTHROPIC_BASE_URL=http://localhost:8080 claude
```

ELIDA automatically groups requests from the same IP into a single session, so all Claude Code requests appear as one session in the dashboard.

### Multi-Backend Routing

ELIDA can route requests to different LLM backends based on model name, headers, or path:

```yaml
# In configs/elida.yaml
backends:
  ollama:
    url: "http://localhost:11434"
    type: ollama
    default: true     # Fallback backend

  openai:
    url: "https://api.openai.com"
    type: openai
    models: ["gpt-*", "o1-*"]  # Route GPT models here

  anthropic:
    url: "https://api.anthropic.com"
    type: anthropic
    models: ["claude-*"]  # Route Claude models here

  routing:
    methods:
      - header   # X-Backend header (highest priority)
      - model    # Model name pattern matching
      - path     # Path prefix (/openai/*, etc.)
      - default  # Fallback
```

Routing priority:
1. **Header**: `curl -H "X-Backend: openai" ...` routes to OpenAI
2. **Model**: Request with `{"model": "gpt-4"}` routes to OpenAI (matches `gpt-*`)
3. **Path**: `/openai/v1/chat/completions` routes to OpenAI backend
4. **Default**: Falls back to the backend marked `default: true`

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

# View flagged sessions (policy violations)
curl http://localhost:9090/control/flagged

# View session history
curl http://localhost:9090/control/history

# Access the dashboard UI
open http://localhost:9090/
```

### Kill Block Modes

When you kill a session, ELIDA blocks subsequent requests from that client. The block duration depends on the configured mode:

| Mode | Behavior | Use Case |
|------|----------|----------|
| `duration` | Blocks for a specific time (e.g., 30m) | Standard rate limiting / cooldown |
| `until_hour_change` | Blocks until the clock hour changes | Aligns with automatic session ID regeneration |
| `permanent` | Blocks until server restart | Maximum security for compromised sessions |

```bash
# Kill a runaway Claude Code session
curl -X POST http://localhost:9090/control/sessions/client-abc123/kill

# Response: {"session_id":"client-abc123","status":"killed"}
# All subsequent requests from that IP will receive 403 until block expires
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
