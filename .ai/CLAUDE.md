# CLAUDE.md

This file provides guidance to Claude Code when working with this repository.

## Project Overview

**ELIDA** (Edge Layer for Intelligent Defense of Agents) is a session-aware reverse proxy for AI traffic. Think of it as a Session Border Controller (SBC) from telecom, but for AI.

## Quick Reference

```bash
# Build and run
make build          # Build binary
make run            # Run with default config
make run-demo       # Run with policy + storage + capture-all

# Testing
make test           # Unit tests
make test-all       # All tests (requires Redis)
make fmt            # Format code (run before commits)

# Verify
go build ./...      # Check compilation
```

## Project Structure

```
elida/
├── cmd/elida/main.go           # Entry point, wiring
├── internal/
│   ├── config/config.go        # Configuration loading, validation
│   ├── session/                # Session management
│   │   ├── session.go          # Session model
│   │   ├── manager.go          # Lifecycle, timeouts, kill switch
│   │   └── store.go            # Store interface (memory/redis)
│   ├── proxy/
│   │   ├── proxy.go            # HTTP proxy handler, streaming
│   │   └── capture.go          # CaptureBuffer for audit mode
│   ├── router/router.go        # Multi-backend routing
│   ├── control/api.go          # REST control API
│   ├── policy/policy.go        # Policy engine, rules
│   ├── websocket/              # WebSocket proxy, voice sessions
│   ├── telemetry/otel.go       # OpenTelemetry integration
│   └── storage/sqlite.go       # SQLite persistence
├── test/
│   ├── unit/                   # Unit tests (black-box)
│   └── integration/            # Integration tests (requires Redis)
├── configs/elida.yaml          # Default configuration
└── deploy/                     # Deployment configs (Helm, ECS, etc.)
```

## Key Patterns

### Session End Callback (main.go)

Uses closure pattern for forward-declaring variables referenced by the session end callback:

```go
var policyEngine *policy.Engine    // Forward declare
var captureBuffer *CaptureBuffer

manager.SetSessionEndCallback(func(snap SessionSnapshot) {
    // Can reference policyEngine, captureBuffer here
    // even though they're initialized later
})

// Initialize after callback is set
policyEngine = policy.NewEngine(...)
```

### Control API Constructor Chain

```go
New() → NewWithHistory() → NewWithPolicy() → NewWithAuth()
```

### Capture Modes

- `flagged_only` (default): Only policy-violated requests captured
- `all`: Every request/response captured (audit/compliance)

## Code Style

- Use `slog` for logging
- Run `make fmt` before commits (CI enforces gofmt)
- Tests in `test/unit/` use black-box testing (exported APIs only)
- No manual struct field alignment (let gofmt handle it)

## Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) — Technical deep-dive
- [SECURITY.md](SECURITY.md) — Security policy, vulnerability reporting
- [docs/AGENTS.md](docs/AGENTS.md) — Autonomous agent monitoring guide
- [docs/ROADMAP.md](docs/ROADMAP.md) — Planned features
- [docs/POLICY_RULES_REFERENCE.md](docs/POLICY_RULES_REFERENCE.md) — Policy rule details
- [docs/SESSION_RECORDS.md](docs/SESSION_RECORDS.md) — SDR/CDR documentation

## Environment Variables

| Variable | Description |
|----------|-------------|
| `ELIDA_LISTEN` | Proxy address (`:8080`) |
| `ELIDA_BACKEND` | Default backend URL |
| `ELIDA_POLICY_ENABLED` | Enable policy engine |
| `ELIDA_STORAGE_ENABLED` | Enable SQLite storage |
| `ELIDA_STORAGE_CAPTURE_MODE` | `flagged_only` or `all` |

See README.md for full list.
