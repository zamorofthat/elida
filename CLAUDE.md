# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

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
│   │   ├── session_test.go     # Session unit tests
│   │   ├── store.go            # Store interface + in-memory impl
│   │   ├── store_test.go       # Store unit tests
│   │   ├── manager.go          # Lifecycle, timeouts, cleanup
│   │   └── manager_test.go     # Manager unit tests
│   ├── proxy/
│   │   ├── proxy.go            # Core proxy logic
│   │   └── proxy_test.go       # Proxy integration tests
│   └── control/
│       ├── api.go              # Control API endpoints
│       └── api_test.go         # Control API tests
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
- [ ] **Redis session store** ← PRIORITY 1
- [ ] **OpenTelemetry integration** ← PRIORITY 2
- [ ] **Multi-backend routing** ← PRIORITY 3
- [ ] SQLite for dashboard history
- [ ] Dashboard UI
- [ ] WebSocket support (for voice/real-time agents)
- [ ] Policy engine
- [ ] Content inspection / PII detection
- [ ] SDK for native agent integration

---

## Scaling Architecture

ELIDA follows the SBC (Session Border Controller) pattern from telecom:

```
                    ┌─────────────────┐
                    │  Load Balancer  │
                    └────────┬────────┘
            ┌────────────────┼────────────────┐
            ▼                ▼                ▼
      ┌──────────┐     ┌──────────┐     ┌──────────┐
      │ ELIDA #1 │     │ ELIDA #2 │     │ ELIDA #3 │
      └────┬─────┘     └────┬─────┘     └────┬─────┘
           │                │                │
           └────────────────┴────────────────┘
                            │
              ┌─────────────┴─────────────┐
              ▼                           ▼
        ┌──────────┐               ┌────────────┐
        │  Redis   │               │    OTel    │
        │          │               │  Collector │
        └──────────┘               └─────┬──────┘
        live state                       │
        kill switch                      ▼
        pub/sub                   Jaeger/Datadog/etc.
```

### Component Responsibilities

| Component | Purpose | Data |
|-----------|---------|------|
| **Redis** | Live session state, shared across instances | Active sessions, kill signals |
| **OpenTelemetry** | Observability, audit trail (like telecom CDRs) | Traces, session lifecycle events |
| **SQLite** | Dashboard history (optional, self-contained UI) | Completed session records |

### Why This Design

1. **Redis for live state** — Any ELIDA instance can see/kill any session
2. **OTel for CDRs** — Session records exported at completion (SBC pattern)
3. **SQLite for dashboard** — Self-contained UI without external dependencies

### Session Lifecycle Flow

```
Request arrives
      │
      ▼
┌─────────────────┐
│ Check Redis for │──▶ Found? Resume session
│ existing session│
└────────┬────────┘
         │ Not found
         ▼
┌─────────────────┐
│ Create session  │──▶ Store in Redis
│ in Redis        │──▶ Start OTel span
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ Proxy request   │──▶ Track bytes, timing
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ Session ends    │──▶ Export CDR via OTel
│ (complete/kill/ │──▶ Write to SQLite (if dashboard enabled)
│  timeout)       │──▶ Cleanup Redis (after retention)
└─────────────────┘
```

---

## Implementation Plan

### Phase 1: Redis Session Store

**Goal:** Enable horizontal scaling with shared session state.

**New files:**
- `internal/session/redis_store.go` — Implements `Store` interface

**Config additions:**
```yaml
session:
  store: redis  # or "memory" (default)
  redis:
    addr: "localhost:6379"
    password: ""
    db: 0
    key_prefix: "elida:session:"
```

**Key features:**
- Store/retrieve sessions as JSON in Redis
- Use Redis TTL for automatic session expiry
- Pub/sub channel for kill signals across instances
- Graceful fallback to memory if Redis unavailable (optional)

**Kill switch flow:**
1. `POST /control/sessions/{id}/kill` received on any instance
2. Publish kill signal to Redis channel `elida:kill:{session_id}`
3. All instances subscribed, close `killChan` for that session
4. Active streaming requests abort

### Phase 2: OpenTelemetry Integration

**Goal:** Export session telemetry for observability and audit.

**New files:**
- `internal/telemetry/otel.go` — OTel setup and span helpers

**Config additions:**
```yaml
telemetry:
  enabled: true
  exporter: otlp  # or "jaeger", "stdout"
  endpoint: "localhost:4317"
  service_name: "elida"
```

**What gets traced:**
- Each proxied request as a span
- Session lifecycle as span events (created, completed, killed, timeout)
- Request/response size as attributes
- Backend target as attribute

**CDR export at session end:**
```json
{
  "session_id": "abc-123",
  "state": "completed",
  "duration_ms": 45000,
  "request_count": 12,
  "bytes_in": 4500,
  "bytes_out": 128000,
  "backend": "https://api.mistral.ai",
  "client_addr": "10.0.0.5"
}
```

### Phase 3: Multi-Backend Routing

**Goal:** Route to different LLM backends based on model/header/path.

(Existing plan in "Next Task: Multi-Backend Routing" section)

### Phase 4: Dashboard UI

**Goal:** Self-contained web UI for monitoring sessions.

**New files:**
- `internal/dashboard/` — Embedded web UI
- `internal/session/sqlite_store.go` — SQLite for history

**Features:**
- Live sessions table (from Redis)
- Session history (from SQLite)
- Kill button
- Stats/charts

---

## Next Task: Redis Session Store

Implement `RedisStore` to enable horizontal scaling.

### Files to Create/Modify

1. **`internal/session/redis_store.go`** — New file
   - Implement `Store` interface
   - JSON serialization for Session struct
   - Redis pub/sub for kill signals

2. **`internal/config/config.go`** — Add Redis config
   ```go
   type RedisConfig struct {
       Addr      string `yaml:"addr"`
       Password  string `yaml:"password"`
       DB        int    `yaml:"db"`
       KeyPrefix string `yaml:"key_prefix"`
   }
   ```

3. **`cmd/elida/main.go`** — Store selection logic
   ```go
   var store session.Store
   if cfg.Session.Store == "redis" {
       store = session.NewRedisStore(cfg.Session.Redis)
   } else {
       store = session.NewMemoryStore()
   }
   ```

### Redis Key Structure

```
elida:session:{session_id}     → Session JSON (with TTL)
elida:sessions                 → Set of active session IDs
elida:kill                     → Pub/sub channel for kill signals
```

### Dependencies to Add

```bash
go get github.com/redis/go-redis/v9
```

---

## Future: Multi-Backend Routing

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

## Build & Test Commands

```bash
# Build and run
make build              # Build binary to bin/elida
make run                # Build and run with default config

# Testing
make test               # Run all tests
make test-coverage      # Run tests with coverage report
go test -v ./internal/session -run TestSessionKill  # Run a single test

# Code quality
make fmt                # Format code
make lint               # Run golangci-lint (requires golangci-lint)

# Quick verification against Ollama
make test-ollama        # Test basic proxy
make test-stream        # Test streaming
make sessions           # View active sessions
make stats              # View stats

# Manual testing
curl http://localhost:8080/api/tags              # Test against Ollama
curl http://localhost:9090/control/sessions      # Check sessions
curl -X POST http://localhost:9090/control/sessions/{id}/kill  # Kill a session
```

## Test Coverage

Tests are organized by package:

| Package | File | Coverage |
|---------|------|----------|
| `internal/session` | `session_test.go` | Session lifecycle: New, Touch, AddBytes, Kill, SetState, Duration, IdleTime, Snapshot |
| `internal/session` | `store_test.go` | MemoryStore: Put, Get, Delete, List, Count, ActiveFilter |
| `internal/session` | `manager_test.go` | Manager: GetOrCreate, GeneratesID, RejectsKilledSession, AllowsTimedOutSessionID, Kill, ListActive, Stats |
| `internal/proxy` | `proxy_test.go` | Proxy: BasicRequest, CustomSessionID, KilledSessionRejected, BackendError, StreamingDetection, BytesTracking, HeadersForwarded |
| `internal/control` | `api_test.go` | Control API: Health, Stats, Sessions list/get, Kill, CORS |

**Key test scenarios:**
- Killed sessions reject new requests (returns 403 with JSON error)
- Custom session IDs are honored
- Session bytes/requests are tracked
- Headers are forwarded to backend
- Streaming vs non-streaming detection works

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
