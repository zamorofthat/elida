# ELIDA - Claude Code Project Guide

## Project Overview

**ELIDA** (Edge Layer for Intelligent Defense of Agents) is a session-aware reverse proxy for AI agents. Think of it as a Session Border Controller (SBC) from telecom, but for AI agent traffic.

### Why It Exists
- Enterprises deploying AI agents need visibility, control, and security
- Security teams need to monitor agent sessions and kill runaway agents
- No standard exists for agent session management—ELIDA aims to define it

### Named After
The developer's grandmother, Elida. Also an acronym: **E**dge **L**ayer for **I**ntelligent **D**efense of **A**gents.

## Tech Stack

- **Language:** Go 1.22+
- **Config:** YAML + environment variable overrides
- **Dependencies:** Minimal (uuid, yaml)
- **Deployment:** Single binary, Docker, Kubernetes

## Project Structure

```
elida/
├── cmd/elida/main.go           # Entry point, server setup
├── internal/
│   ├── config/config.go        # Configuration loading
│   ├── session/
│   │   ├── session.go          # Session model
│   │   ├── store.go            # Store interface + in-memory impl
│   │   └── manager.go          # Lifecycle, timeouts, cleanup
│   ├── proxy/proxy.go          # Core proxy logic
│   └── control/api.go          # Control API endpoints
├── configs/elida.yaml          # Default configuration
├── Dockerfile
├── Makefile
└── README.md
```

## Key Concepts

### Sessions
- Identified by `X-Session-ID` header (generated if missing)
- States: `Active`, `Completed`, `Killed`, `TimedOut`
- Track: requests, bytes in/out, duration, idle time
- Can be killed via control API (closes `killChan`)

### Proxy
- Handles HTTP, NDJSON streaming (Ollama), SSE streaming (OpenAI/Anthropic/Mistral)
- Captures request/response bodies for logging
- Forwards `X-Session-ID` in responses

### Control API (port 9090)
- `GET /control/health` — Health check
- `GET /control/stats` — Session statistics
- `GET /control/sessions` — List sessions
- `GET /control/sessions/{id}` — Session details
- `POST /control/sessions/{id}/kill` — Kill a session

## Current State (MVP)

### Implemented
- [x] HTTP reverse proxy
- [x] NDJSON streaming (Ollama)
- [x] SSE streaming (OpenAI, Anthropic, Mistral)
- [x] Session tracking and management
- [x] Session timeout enforcement
- [x] Kill switch for active sessions
- [x] Control API
- [x] Structured JSON logging
- [x] Single backend configuration

### Not Yet Implemented
- [ ] **Multi-backend routing** ← NEXT PRIORITY
- [ ] WebSocket support (for voice/real-time agents)
- [ ] Policy engine
- [ ] Content inspection / PII detection
- [ ] OpenTelemetry integration
- [ ] Dashboard UI
- [ ] Redis-backed session store
- [ ] SDK for native agent integration

## Next Task: Multi-Backend Routing

Add support for routing to multiple backends based on:
1. `X-Backend` header (highest priority)
2. Model name pattern matching (`gpt-*` → OpenAI, `claude-*` → Anthropic)
3. Path prefix (`/openai/*`, `/anthropic/*`)
4. Default backend (fallback)

### Proposed Config Structure

```yaml
backends:
  ollama:
    url: "http://localhost:11434"
    type: ollama
    default: true
    
  openai:
    url: "https://api.openai.com"
    type: openai
    models: ["gpt-*"]
    
  anthropic:
    url: "https://api.anthropic.com"
    type: anthropic
    models: ["claude-*"]
    
  mistral:
    url: "https://api.mistral.ai"
    type: mistral
    models: ["mistral-*", "codestral-*"]

routing:
  methods:
    - header    # X-Backend header
    - model     # Match by model name in request body
    - path      # Path prefix
    - default   # Fallback
```

### Implementation Notes
- Need to parse request body to extract model name
- Model patterns use glob matching
- Each backend needs its own http.Transport for connection pooling
- Router should be a new package: `internal/router/`

## Code Style

- Standard Go conventions
- `slog` for structured logging
- Interfaces for testability (see `session.Store`)
- Context for cancellation
- Graceful shutdown handling

## Testing

```bash
# Build
make build

# Run locally
make run

# Test against Ollama
curl http://localhost:8080/api/tags

# Check sessions
curl http://localhost:9090/control/sessions

# Kill a session
curl -X POST http://localhost:9090/control/sessions/{id}/kill
```

## Environment Variables

- `ELIDA_LISTEN` — Proxy listen address (default: `:8080`)
- `ELIDA_BACKEND` — Backend URL (default: `http://localhost:11434`)
- `ELIDA_CONTROL_LISTEN` — Control API address (default: `:9090`)
- `ELIDA_LOG_LEVEL` — Log level: debug, info, warn, error

## Architecture Decisions

1. **Reverse proxy over SDK** — Works with any agent without code changes
2. **Go over Rust** — Faster iteration, developer knows Go, performance is sufficient
3. **In-memory store first** — Simple, swap to Redis later via interface
4. **Session-centric model** — Inspired by telecom SBCs, not request-centric like API gateways
5. **Fail-open for MVP** — Backend errors don't kill sessions, just log

## Related Context

This project started from an existing Kubernetes LLM cluster setup (coffee-chats) that uses:
- Kustomize for deployments
- OpenResty/nginx with Lua for request logging
- Ollama for local LLM inference
- OpenLIT for observability

ELIDA replaces the nginx layer with more capabilities.

## Commands for Development

```bash
# Format code
make fmt

# Run tests
make test

# Build Docker image
make docker

# View active sessions
make sessions

# View stats
make stats
```
