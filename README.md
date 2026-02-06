# ELIDA

**Edge Layer for Intelligent Defense of Agents**

ELIDA is a session-aware proxy for AI agents. It provides visibility, control, and security for agent-to-agent and agent-to-LLM communication.

## Why ELIDA?

As enterprises deploy AI agents, security teams need:
- **Visibility** — See what agents are doing in real-time
- **Control** — Kill runaway sessions, enforce timeouts
- **Audit** — Complete session logs for compliance
- **Protection** — Policy enforcement with 40+ OWASP LLM Top 10 rules

Think of it like a Session Border Controller (SBC) from telecom, but for AI agents.

## Features

### Current Features
- [x] HTTP reverse proxy with request/response capture
- [x] Streaming support (NDJSON for Ollama, SSE for OpenAI/Anthropic/Mistral)
- [x] WebSocket proxy for voice/real-time agents (OpenAI Realtime, Deepgram, ElevenLabs)
- [x] Voice session tracking with SIP-inspired lifecycle (INVITE/BYE/Hold/Resume)
- [x] Voice CDR persistence with full transcripts
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
- [x] Capture-all mode for full audit/compliance (every request/response)

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
- [x] Response body capture for flagged sessions (forensics)
- [x] Immediate persistence of flagged sessions (crash-safe)

### Roadmap
- [ ] Real-time speech analytics (live sentiment/coaching during voice sessions)
- [ ] LLM-as-judge content moderation (local Gemma/ShieldGemma models)
- [ ] Per-model rate limits
- [ ] SDK for native agent integration

## Quick Start

### Prerequisites
- Go 1.24+
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

### WebSocket / Voice Sessions

ELIDA supports WebSocket proxying for real-time voice AI agents:

```yaml
websocket:
  enabled: true
  voice_sessions:
    enabled: true
    max_concurrent: 5
    protocols:
      - openai_realtime  # OpenAI Realtime API
      - deepgram         # Deepgram STT
      - elevenlabs       # ElevenLabs TTS
```

Voice sessions use a SIP-inspired lifecycle:
- **INVITE** → Session starts (detected from protocol messages)
- **Active** → Conversation in progress, transcripts captured
- **Hold/Resume** → Pause and continue
- **BYE** → Session ends, CDR persisted with full transcript

```bash
# Run with WebSocket enabled
make run-websocket

# Run with WebSocket + policy scanning
make run-websocket-policy

# Test with mock voice server (no API keys needed)
make mock-voice  # Terminal 1
make run-websocket  # Terminal 2
wscat -c ws://localhost:8080  # Terminal 3
```

### Capture-All Mode

For audit/compliance, capture every request/response body (not just policy-flagged):

```yaml
storage:
  enabled: true
  capture_mode: "all"             # or "flagged_only" (default)
  max_capture_size: 10000         # 10KB per body
  max_captured_per_session: 100   # Max pairs per session
```

```bash
# Run with capture-all mode
make run-demo

# Or via environment variables
ELIDA_STORAGE_ENABLED=true ELIDA_STORAGE_CAPTURE_MODE=all ./bin/elida
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

# Resume a killed session
curl -X POST http://localhost:9090/control/sessions/{session-id}/resume

# View flagged sessions (policy violations)
curl http://localhost:9090/control/flagged

# View session history
curl http://localhost:9090/control/history

# Voice sessions (WebSocket - live)
curl http://localhost:9090/control/voice

# Voice session history (persisted CDRs with transcripts)
curl http://localhost:9090/control/voice-history

# TTS request tracking
curl http://localhost:9090/control/tts

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
┌──────────────────────────────────────────────────────────────────┐
│                            ELIDA                                  │
├──────────────┬──────────────┬──────────────┬─────────────────────┤
│    Proxy     │  WebSocket   │   Session    │    Control API      │
│   Handler    │   Handler    │   Manager    │  GET  /sessions     │
│              │              │              │  POST /kill         │
│  HTTP/NDJSON │  Voice       │  Lifecycle   │  GET  /voice        │
│  SSE         │  Sessions    │  Timeouts    │  GET  /flagged      │
│              │  Transcripts │  Cleanup     │  GET  /history      │
├──────────────┴──────────────┴──────────────┴─────────────────────┤
│     Policy Engine (40+ rules, OWASP LLM Top 10)                   │
├───────────────────────────────────────────────────────────────────┤
│     Session Store (In-Memory / Redis) + SQLite History            │
└───────────────────────────────────────────────────────────────────┘
           │                              │
           ▼                              ▼
    ┌─────────────┐               ┌─────────────────────────┐
    │   Agents    │               │       Backends          │
    │  (Clients)  │               │  Ollama, OpenAI,        │
    │             │               │  Anthropic, Mistral,    │
    │             │               │  Deepgram, ElevenLabs   │
    └─────────────┘               └─────────────────────────┘
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ELIDA_LISTEN` | `:8080` | Proxy listen address |
| `ELIDA_BACKEND` | `http://localhost:11434` | Backend URL |
| `ELIDA_CONTROL_LISTEN` | `:9090` | Control API address |
| `ELIDA_SESSION_STORE` | `memory` | Session store: `memory` or `redis` |
| `ELIDA_POLICY_ENABLED` | `false` | Enable policy engine |
| `ELIDA_POLICY_MODE` | `enforce` | Policy mode: `enforce` or `audit` |
| `ELIDA_POLICY_PRESET` | - | Policy preset: `minimal`, `standard`, `strict` |
| `ELIDA_STORAGE_ENABLED` | `false` | Enable SQLite storage |
| `ELIDA_STORAGE_CAPTURE_MODE` | `flagged_only` | Capture mode: `flagged_only` or `all` |
| `ELIDA_WEBSOCKET_ENABLED` | `false` | Enable WebSocket proxy |
| `ELIDA_TLS_ENABLED` | `false` | Enable TLS/HTTPS |
| `ELIDA_TELEMETRY_ENABLED` | `false` | Enable OpenTelemetry |

## Benchmarking

ELIDA includes a benchmark suite for performance testing:

```bash
# Run all benchmarks
./scripts/benchmark.sh

# Compare policy modes (no-policy vs audit vs enforce)
./scripts/benchmark.sh --compare-modes
```

### Sample Results

| Metric | No Policy | Audit | Enforce |
|--------|-----------|-------|---------|
| Avg latency (ms) | 109 | 116 | 113 |
| Blocked req latency (ms) | 107 | 100 | **49** |
| Memory per session (KB) | 6 | 0 | 30 |

**Key insights:**
- **Enforce mode**: Blocked requests are ~2x faster (no backend call)
- **Memory**: ~25-30KB per session with content capture enabled
- **10K sessions**: ~267MB projected memory usage

### Target Performance

- 10K concurrent sessions per node
- <50KB memory per session
- Horizontal scaling via Redis

## Development

```bash
# Build and run
make build              # Build binary
make run                # Run with default config
make run-demo           # Run with policy + storage + capture-all

# Testing
make test               # Unit tests (87 tests)
make test-all           # All tests including integration (requires Redis)

# Code quality
make fmt                # Format code
make lint               # Run linter (requires golangci-lint)

# Docker
make docker             # Build Docker image
make up                 # Start full stack (Redis + ELIDA)
make down               # Stop stack

# WebSocket / Voice
make mock-voice         # Start mock voice server
make run-websocket      # Run with WebSocket enabled

# Useful shortcuts
make sessions           # View active sessions
make stats              # View stats
make health             # Health check
make history            # View session history
```

## License

MIT

## Why "ELIDA"?

**E**dge **L**ayer for **I**ntelligent **D**efense of **A**gents

