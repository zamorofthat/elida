# ELIDA

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/)
[![CI](https://github.com/zamorofthat/elida/actions/workflows/ci.yml/badge.svg)](https://github.com/zamorofthat/elida/actions/workflows/ci.yml)

**Edge Layer for Intelligent Defense of Agents**

ELIDA is a session-aware reverse proxy for AI agents. It provides visibility, control, and security for AI model API traffic—like a Session Border Controller (SBC) from telecom, but for AI.

## Why ELIDA?

As enterprises deploy AI agents, security teams need:
- **Visibility** — See what agents are doing in real-time
- **Control** — Kill runaway sessions, enforce timeouts
- **Audit** — Complete session logs for compliance
- **Protection** — Policy enforcement with 40+ OWASP LLM Top 10 rules

## Quick Start

```bash
# Clone and build
git clone https://github.com/zamorofthat/elida.git
cd elida && make build

# Run (proxies to localhost:11434 by default)
make run

# Or with Docker
docker run -p 8080:8080 -p 9090:9090 ghcr.io/zamorofthat/elida:latest
```

Point your AI client at ELIDA:
```bash
# Claude Code
ANTHROPIC_BASE_URL=http://localhost:8080 claude

# OpenAI-compatible tools
OPENAI_BASE_URL=http://localhost:8080 your-tool
```

Open the dashboard at `http://localhost:9090`

## Features

**Core**
- HTTP/WebSocket reverse proxy with streaming support
- Multi-backend routing (Ollama, OpenAI, Anthropic, Mistral)
- Session tracking with kill/resume lifecycle
- Redis-backed session store for horizontal scaling

**Security**
- 40+ OWASP LLM Top 10 policy rules
- Prompt injection detection (LLM01)
- PII and credential detection (LLM06)
- Tool abuse prevention (LLM07/08)

**Enterprise (v0.2.0)**
- Risk ladder with progressive escalation
- Token burn rate tracking with circuit breaker
- Immutable event stream with PII redaction
- Chaos suite for policy benchmarking

## Configuration

```yaml
# configs/elida.yaml
listen: ":8080"
backend: "https://api.anthropic.com"

session:
  timeout: 5m

policy:
  enabled: true
  preset: standard  # minimal | standard | strict
```

Or use environment variables:
```bash
ELIDA_BACKEND=https://api.anthropic.com ELIDA_POLICY_ENABLED=true ./bin/elida
```

See [Configuration Guide](docs/CONFIGURATION.md) for full options.

## Control API

```bash
curl http://localhost:9090/control/sessions        # List sessions
curl http://localhost:9090/control/flagged         # Policy violations
curl -X POST http://localhost:9090/control/sessions/{id}/kill  # Kill session
curl http://localhost:9090/control/events          # Audit log
```

See [API Reference](docs/API.md) for all endpoints.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                          ELIDA                               │
├─────────────┬─────────────┬─────────────┬───────────────────┤
│   Proxy     │  WebSocket  │  Session    │   Control API     │
│   Handler   │  Handler    │  Manager    │   + Dashboard     │
├─────────────┴─────────────┴─────────────┴───────────────────┤
│          Policy Engine (40+ OWASP LLM Top 10 rules)         │
├─────────────────────────────────────────────────────────────┤
│      Session Store (Memory/Redis) + SQLite History          │
└─────────────────────────────────────────────────────────────┘
         │                              │
         ▼                              ▼
   ┌───────────┐               ┌────────────────┐
   │  Agents   │               │    Backends    │
   │ (Clients) │               │ Ollama/OpenAI/ │
   │           │               │ Anthropic/etc  │
   └───────────┘               └────────────────┘
```

## Documentation

| Guide | Description |
|-------|-------------|
| [Getting Started](docs/GETTING_STARTED.md) | Step-by-step tutorial |
| [Configuration](docs/CONFIGURATION.md) | YAML and environment variable options |
| [API Reference](docs/API.md) | Control API endpoints |
| [Policy Rules](docs/POLICY_RULES_REFERENCE.md) | All 40+ built-in security rules |
| [Telco Controls](docs/TELCO_CONTROLS.md) | Risk ladder, token tracking, events |
| [Architecture](docs/ARCHITECTURE.md) | Technical deep-dive |
| [Enterprise Deployment](docs/ENTERPRISE_DEPLOYMENT.md) | Kubernetes, Helm, fleet management |
| [Security Controls](docs/SECURITY_CONTROLS.md) | OWASP/NIST mappings for auditors |

## Development

```bash
make build          # Build binary
make test           # Run tests
make run-demo       # Run with policy + storage
make docker         # Build Docker image
```

## License

Apache License 2.0 — See [LICENSE](LICENSE)

## Why "ELIDA"?

Named after my grandmother. Also: **E**dge **L**ayer for **I**ntelligent **D**efense of **A**gents
