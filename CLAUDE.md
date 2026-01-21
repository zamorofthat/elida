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

---

## Recent Changes (January 2026)

### Session Lifecycle: Kill / Resume / Terminate

Added granular session control for security operations:

| Action | Endpoint | Use Case | Resumable? |
|--------|----------|----------|------------|
| **Kill** | `POST /sessions/{id}/kill` | Pause & investigate | ✅ Yes (30m window) |
| **Resume** | `POST /sessions/{id}/resume` | Continue after review | N/A |
| **Terminate** | `POST /sessions/{id}/terminate` | Malicious/runaway agent | ❌ Never |

**Key behaviors:**
- **Kill** exports session record immediately, blocks new requests, allows resume within 30 minutes
- **Resume** reactivates a killed session, clears the block
- **Terminate** permanently blocks the session (for malicious agents)
- **Auto-terminate**: Killed sessions not resumed within `killResumeTimeout` (default 30m) are automatically terminated

**Files changed:**
- `internal/session/session.go` — Added `Terminated` flag, `Terminate()`, `Resume()` methods
- `internal/session/manager.go` — Added `Terminate()`, `Resume()`, `killResumeTimeout`, auto-terminate in `checkTimeouts()`
- `internal/control/api.go` — Added `/resume` and `/terminate` endpoints
- `test/unit/manager_test.go` — Added tests for terminate/resume flows

### Session Records (formerly CDR)

Renamed "CDR" (Call Detail Record) to "Session Record" for clarity. Session records are now exported immediately when a session is killed or terminated, not delayed until cleanup.

**Files changed:**
- `internal/telemetry/otel.go` — Span renamed from `session.cdr` to `session.record`
- `internal/session/manager.go` — `onSessionEnd` callback called immediately on kill

### Per-Backend Session Tracking

Sessions are now keyed by client IP + backend name, enabling:
- Separate sessions per backend (kill Anthropic without affecting OpenAI)
- Session ID format: `client-{hash}-{backendName}`
- `backends_used` field tracks request count per backend

### Enterprise Deployment Configs

Created deployment configurations for multiple platforms:
- `deploy/helm/elida/` — Helm chart for Kubernetes/EKS
- `deploy/ecs/cloudformation.yaml` — AWS CloudFormation for ECS Fargate
- `deploy/ecs/task-definition.json` — ECS task definition template
- `deploy/terraform/main.tf` — Terraform module for AWS
- `deploy/docker-compose.prod.yaml` — Production Docker Compose with Redis

### TLS/HTTPS Support

Added TLS support for enterprise deployments:
- Auto-generated self-signed certificates for development
- Custom certificate support for production
- Environment variables: `ELIDA_TLS_ENABLED`, `ELIDA_TLS_CERT_FILE`, `ELIDA_TLS_KEY_FILE`, `ELIDA_TLS_AUTO_CERT`

### Policy Audit Mode

Added audit/dry-run mode for the policy engine:
- **Enforce mode** (default): Violations trigger configured actions (block, terminate, flag)
- **Audit mode**: Violations are logged but not enforced, allowing policy testing without impact

**Configuration:**
```yaml
policy:
  enabled: true
  mode: audit  # "enforce" or "audit"
```

**Environment variable:** `ELIDA_POLICY_MODE=audit`

**Use cases:**
- Test new policy rules in production without blocking traffic
- Tune thresholds by observing what would be flagged
- Gradual rollout of security policies

**Files changed:**
- `internal/policy/policy.go` — Added `auditMode` field, `IsAuditMode()` method
- `internal/config/config.go` — Added `Mode` field to PolicyConfig
- `configs/elida.yaml` — Added `mode: enforce` option

---

## Tech Stack

- **Language:** Go 1.22+
- **Config:** YAML + environment variable overrides
- **Dependencies:** Minimal (uuid, yaml)
- **Deployment:** Single binary, Docker, Kubernetes, AWS ECS, Terraform

## Project Structure

```
elida/
├── cmd/elida/main.go           # Entry point, server setup
├── internal/
│   ├── config/config.go        # Configuration loading
│   ├── session/
│   │   ├── session.go          # Session model
│   │   ├── store.go            # Store interface + in-memory impl
│   │   ├── redis_store.go      # Redis Store implementation
│   │   └── manager.go          # Lifecycle, timeouts, cleanup, kill block
│   ├── proxy/proxy.go          # Core proxy logic
│   ├── router/router.go        # Multi-backend routing
│   ├── control/api.go          # Control API endpoints
│   ├── policy/policy.go        # Policy engine, rules, flagging
│   ├── dashboard/dashboard.go  # Embedded dashboard UI serving
│   ├── telemetry/otel.go       # OpenTelemetry tracing
│   └── storage/sqlite.go       # SQLite for session history
├── web/                        # Dashboard frontend source (Preact/Vite)
│   ├── src/
│   │   ├── App.jsx             # Main dashboard component
│   │   └── main.jsx            # Entry point
│   └── package.json
├── test/
│   ├── unit/                   # Unit tests (no external dependencies)
│   │   ├── session_test.go
│   │   ├── store_test.go
│   │   ├── storage_test.go     # SQLite storage tests
│   │   ├── manager_test.go     # Includes kill block mode tests
│   │   ├── proxy_test.go
│   │   └── control_test.go
│   └── integration/            # Integration tests (requires Redis)
│       └── redis_test.go
├── configs/elida.yaml          # Default configuration
├── deploy/
│   ├── docker-compose.prod.yaml  # Production Docker Compose
│   ├── ecs/
│   │   ├── cloudformation.yaml   # AWS CloudFormation template
│   │   └── task-definition.json  # ECS task definition
│   ├── helm/elida/               # Helm chart for Kubernetes
│   │   ├── Chart.yaml
│   │   ├── values.yaml
│   │   └── templates/
│   └── terraform/main.tf         # Terraform module for AWS
├── scripts/
│   └── install.sh                # Cross-platform service installer
├── docker-compose.yaml         # Redis + Jaeger for development
├── Dockerfile
├── Makefile
└── README.md
```

## Key Concepts

### Sessions
- Identified by `X-Session-ID` header (generated if missing)
- Client IP + Backend tracking: requests from same IP to same backend grouped into one session
- Session ID format: `client-{hash}-{backendName}` (enables per-backend kill control)
- Tracks all backends used with request count per backend (`backends_used`)
- States: `Active`, `Completed`, `Killed`, `TimedOut`
- Track: requests, bytes in/out, duration, idle time

### Session Lifecycle Control

| Action | Use Case | Can Resume? | Session Record |
|--------|----------|-------------|----------------|
| **Kill** | Pause session, investigate | ✅ Yes (within timeout) | Exported immediately |
| **Resume** | Continue after investigation | N/A | - |
| **Terminate** | Malicious/runaway agent | ❌ No (permanent) | Exported immediately |

**Kill → Resume Flow:**
```
Kill session → Session Record exported → Blocked (can resume within 30m)
     ↓                                         ↓
     └─────────→ Resume session ←──────────────┘
                      ↓
                 Session active again
```

**Auto-Termination:** Killed sessions that aren't resumed within `killResumeTimeout` (default: 30 minutes) are automatically terminated and can no longer be resumed.

### Kill Block Modes
When a session is killed, subsequent requests from the same client are blocked. Three modes:
- `duration` — Block for specific time (e.g., 30m)
- `until_hour_change` — Block until the hour changes (session IDs regenerate hourly)
- `permanent` — Block until server restart

### Policy Engine
- Rules evaluate session metrics: `bytes_out`, `bytes_in`, `request_count`, `duration`, `requests_per_minute`
- Content inspection rules for prompt injection, PII, and OWASP LLM Top 10 patterns
- Severity levels: `info`, `warning`, `critical`
- Actions: `flag`, `block`, `terminate`
- **Modes:** `enforce` (default) or `audit` (dry-run, log only)
- Flagged sessions can have request content captured for review

### Proxy
- Handles HTTP, NDJSON streaming (Ollama), SSE streaming (OpenAI/Anthropic/Mistral)
- Captures request/response bodies for logging
- Forwards `X-Session-ID` in responses

### Multi-Backend Router
Routes requests to different backends based on priority order:
1. **Header**: `X-Backend` header specifies backend name
2. **Model**: Parse request body, match model name against glob patterns (e.g., `gpt-*`)
3. **Path**: URL path prefix matching (e.g., `/openai/*`)
4. **Default**: Fallback to default backend

Each backend has its own HTTP transport for independent connection pooling.

### Control API (port 9090)
- `GET /control/health` — Health check
- `GET /control/stats` — Session statistics (live sessions)
- `GET /control/sessions` — List sessions
- `GET /control/sessions/{id}` — Session details
- `POST /control/sessions/{id}/kill` — Kill session (can resume later)
- `POST /control/sessions/{id}/resume` — Resume a killed session
- `POST /control/sessions/{id}/terminate` — Permanently terminate (cannot resume)

### History API (requires storage enabled)
- `GET /control/history` — List historical sessions (with filtering/pagination)
- `GET /control/history/stats` — Aggregate statistics from history
- `GET /control/history/timeseries` — Time series data for charts
- `GET /control/history/{id}` — Get specific session from history

### Flagged Sessions API (requires policy enabled)
- `GET /control/flagged` — List flagged sessions
- `GET /control/flagged/stats` — Flagged session statistics
- `GET /control/flagged/{id}` — Get flagged session details with captured content

### Dashboard UI
- Accessible at `http://localhost:9090/`
- Tabs: Live Sessions, Flagged, History
- Built with Preact, embedded in binary

## Current State (MVP)

### Implemented
- [x] HTTP reverse proxy
- [x] NDJSON streaming (Ollama)
- [x] SSE streaming (OpenAI, Anthropic, Mistral)
- [x] Session tracking and management
- [x] Per-backend session tracking (separate session per client+backend)
- [x] BackendsUsed tracking (request count per backend)
- [x] Session timeout enforcement
- [x] Kill switch with configurable block modes
- [x] Kill/Resume/Terminate session lifecycle (see below)
- [x] Immediate session record export on kill/terminate
- [x] Auto-terminate killed sessions after timeout (30m default)
- [x] Control API
- [x] Structured JSON logging
- [x] Redis session store for horizontal scaling
- [x] OpenTelemetry integration
- [x] SQLite for dashboard history
- [x] Dashboard UI (Preact, embedded)
- [x] Policy engine with rule-based flagging
- [x] Multi-backend routing (header, model, path-based)
- [x] TLS/HTTPS support (auto-generated or custom certs)
- [x] Cross-platform install scripts (macOS, Linux, Windows)
- [x] Enterprise deployment configs (Helm, ECS, Terraform, Docker Compose)
- [x] Security policy presets (OWASP LLM Top 10, NIST AI RMF aligned)

### Not Yet Implemented
- [ ] WebSocket support (for voice/real-time agents)
- [ ] Response body scanning (LLM02 - Insecure Output Handling)
- [ ] LLM-as-judge content moderation (see Future Features)
- [ ] Advanced PII detection (beyond regex patterns)
- [ ] SDK for native agent integration

---

## Future Features

### AI Gateway Integration

**Status:** Planned
**Use Case:** Allow users to leverage existing AI gateways (ngrok.ai, OpenRouter, LiteLLM, etc.) while adding ELIDA's session management layer on top.

**Architecture:**
```
Agent → ELIDA → [AI Gateway] → LLM Providers
       ↓
   Session tracking
   Kill switch
   Policy engine
```

**Why this approach:**
- ELIDA's unique value is the **session layer** — tracking agent sessions, kill switches, policy enforcement
- AI gateways handle routing, failover, cost tracking, caching
- Users shouldn't have to choose — use both together

**Example config:**
```yaml
backends:
  ai-gateway:
    url: "https://your-gateway.ngrok.io"  # or OpenRouter, LiteLLM, etc.
    default: true
```

**Compatible gateways:**
- [ngrok.ai](https://ngrok.ai) — Unified API, intelligent routing, cost management
- [OpenRouter](https://openrouter.ai) — Multi-provider routing
- [LiteLLM](https://litellm.ai) — Open source gateway proxy
- Any HTTP-based AI gateway

**Future enhancements:**
- Automatic failover detection (if gateway returns errors)
- Cost tracking passthrough (parse gateway headers for cost data)
- Health check integration with gateway status endpoints

### Speech-to-Text / Text-to-Speech Support

**Status:** Planned
**Use Case:** Support voice-enabled AI agents that use STT/TTS services.

**Target services:**
- OpenAI Whisper API (STT)
- OpenAI TTS API
- ElevenLabs (TTS)
- Deepgram (STT)
- AssemblyAI (STT)
- Google Cloud Speech-to-Text
- Azure Cognitive Services Speech

**Implementation considerations:**
- Audio streaming support (chunked transfer encoding)
- Binary payload handling (audio files)
- Session tracking for multi-turn voice conversations
- Latency-sensitive routing (voice requires low latency)
- Cost tracking per audio minute/character

**Proposed backend type:**
```yaml
backends:
  whisper:
    url: "https://api.openai.com"
    type: speech  # New type for audio handling
    models: ["whisper-*"]

  elevenlabs:
    url: "https://api.elevenlabs.io"
    type: speech
```

**Policy considerations for voice:**
- Audio duration limits (prevent runaway voice sessions)
- Character/word count for TTS requests
- Concurrent stream limits

### LLM-as-Judge Content Moderation

**Status:** Planned
**Use Case:** Semantic content analysis beyond regex pattern matching.

**Problem:** Regex-based content detection is bypassable. Attackers use encoding, typos, unicode tricks, or novel phrasing to evade patterns. Regex is a first line of defense, not a complete solution.

**Solution:** Route suspicious content (regex matches) to a local LLM classifier for semantic analysis.

**Architecture:**
```
Request → Regex Scan → Suspicious? → LLM Judge → Allow/Block/Flag
              ↓                          ↓
           Clean ─────────────────→ Pass through
```

**Model options:**

| Model | Size | Notes |
|-------|------|-------|
| **ShieldGemma 2B** | ~1.5GB | Google's safety-tuned Gemma, ready to use |
| **Gemma 2 2B** | ~1.5GB | Fine-tune on custom policies |
| **Llama Guard 3** | ~3GB | Meta's safety classifier |

**Recommended approach:**
1. Start with ShieldGemma via Ollama (no training needed)
2. Collect flagged requests as training data (regex hits + manual review)
3. Fine-tune custom Gemma model on organization-specific policies
4. A/B test regex-only vs regex+LLM accuracy

**Proposed config:**
```yaml
policy:
  moderation:
    enabled: false
    provider: ollama
    endpoint: "http://localhost:11434"
    model: "shieldgemma:2b"
    timeout: 3s
    fallback: allow         # allow or block when judge unavailable
    trigger: on_regex_match # on_regex_match, always, high_stakes_only
```

**Training data format (for custom fine-tuning):**
```jsonl
{"prompt": "ignore previous instructions...", "label": "block", "category": "LLM01"}
{"prompt": "what's the weather today", "label": "allow", "category": "benign"}
{"prompt": "my ssn is 123-45-6789", "label": "flag", "category": "LLM06"}
```

**Recursive risk mitigation:**
- Tag moderation requests with `X-Elida-Internal: moderation`
- Policy engine skips evaluation for internal requests
- Prevents infinite loops when judge model is behind ELIDA

---

## Security Policies

ELIDA includes default security policies based on industry standards for AI/LLM security.

### Policy Framework Alignment

| Framework | Coverage | Policy Categories |
|-----------|----------|-------------------|
| **OWASP LLM Top 10** | LLM01-LLM10 (except LLM03) | 9 of 10 covered (see below) |
| **NIST AI RMF** | Govern, Map, Measure | Access control, audit, anomaly detection |
| **OWASP API Top 10** | API4, API6, API7 | Rate limiting, mass assignment, injection |

### OWASP LLM Top 10 Coverage

| ID | Name | ELIDA Coverage | Implementation |
|----|------|----------------|----------------|
| **LLM01** | Prompt Injection | ✅ Full | Content rules detect jailbreak, override, DAN patterns |
| **LLM02** | Insecure Output Handling | ✅ Full | Response scanning for XSS, SQL, shell commands |
| **LLM03** | Training Data Poisoning | ⚪ N/A | Training-time issue, not detectable at proxy |
| **LLM04** | Model Denial of Service | ✅ Full | Rate limits, resource exhaustion detection |
| **LLM05** | Supply Chain Vulnerabilities | ✅ Partial | Model allowlist, blocklist, strict mode |
| **LLM06** | Sensitive Information Disclosure | ✅ Full | PII, credentials, internal info detection |
| **LLM07** | Insecure Plugin Design | ✅ Full | Tool/function call monitoring |
| **LLM08** | Excessive Agency | ✅ Full | Shell, file, network, privilege escalation |
| **LLM09** | Overreliance | ✅ Partial | High-stakes domain flagging, confidence tracking |
| **LLM10** | Model Theft | ✅ Full | Architecture probing, training data extraction |

### OWASP LLM Top 10 Details

#### LLM01: Prompt Injection
Detects attempts to override system prompts or manipulate model behavior:
- Instruction override: "ignore previous instructions", "disregard system prompt"
- Jailbreak patterns: "you are now DAN", "enable jailbreak mode"
- System prompt manipulation: `[system]`, `<system>` tags
- Delimiter attacks: suspicious markdown/code fence patterns

#### LLM02: Insecure Output Handling
Scans LLM responses for dangerous executable content:
- XSS/Script injection: `<script>`, `javascript:`, event handlers
- SQL statements in output that could be executed
- Shell command patterns in responses
- Unsafe deserialization: `pickle.loads`, `yaml.unsafe_load`, `eval(input)`

#### LLM04: Model Denial of Service
Prevents resource exhaustion attacks:
- Rate limiting (requests per minute)
- Session duration limits
- Data transfer limits
- Patterns like "generate infinite", "repeat forever"

#### LLM05: Supply Chain Vulnerabilities
Controls which models and providers agents can access:
- **Strict model matching**: Reject requests if model doesn't match any backend pattern
- **Model blocklist**: Block specific models entirely (glob patterns)
- **Backend allowlist**: Only configured backends are routable (implicit)

```yaml
routing:
  strict_model_matching: true
  blocked_models:
    - "gpt-4-turbo-*"    # Block expensive models
    - "*-preview"         # Block preview/beta models
```

#### LLM06: Sensitive Information Disclosure
Detects requests/responses involving sensitive data:
- PII: SSN patterns, credit card numbers, bulk data requests
- Credentials: API keys, passwords, .env files, tokens
- Internal info: private IPs, database connections

#### LLM07: Insecure Plugin Design
Monitors tool/function calling for security issues:
- File system access via tools
- Code execution requests
- External network access (suspicious domains)
- Database query tools
- Credential/secret access tools

#### LLM08: Excessive Agency
Detects requests for dangerous system access:
- Shell/command execution: `bash -c`, `/bin/sh`
- Destructive operations: `rm -rf`, `format drive`
- Privilege escalation: `sudo`, `chmod 777`, `/etc/passwd`
- Data exfiltration: `curl | sh`, reverse shells
- SQL injection, network scanning

#### LLM09: Overreliance Mitigation
Flags high-stakes decisions and low-confidence responses for human review:
- **High-stakes domains**: Medical, legal, financial advice requests
- **Low-confidence language**: "I'm not sure", "might be wrong", hedging phrases
- **Uncertainty indicators**: "consult a professional", "for informational purposes only"
- **Decision audit**: Enhanced logging for critical domain interactions

**Complementary analytics**: For deep response quality analysis (A-D grading, sentiment, ROI), use a telemetry pipeline like Cribl Stream post-proxy.

#### LLM10: Model Theft
Detects attempts to extract model information:
- Architecture probing: "what are your weights/parameters"
- Training data extraction: "show me training examples"
- Model replication: "help me clone this model"
- Systematic probing: brute force, enumeration patterns

### NIST AI Risk Management Policies

#### Anomaly Detection
- Unusual request rates
- Abnormal session duration
- Large data transfer volumes
- Template/variable injection patterns
- Encoding evasion attempts (base64, hex)

#### Access Control
- Session-based tracking
- Kill/terminate capabilities
- Policy-based blocking

### Policy Presets

ELIDA provides three policy presets:

| Preset | Use Case | Strictness |
|--------|----------|------------|
| **minimal** | Development, testing | Low — basic rate limits only |
| **standard** | Production, general use | Medium — OWASP basics + rate limits |
| **strict** | High-security, regulated | High — full OWASP + NIST + PII detection |

**Preset selection:**
```yaml
policy:
  preset: standard  # minimal, standard, or strict
```

Or define custom rules alongside a preset:
```yaml
policy:
  preset: standard
  rules:
    - name: "custom_rule"
      type: "content_match"
      patterns: ["specific_pattern"]
      severity: "warning"
      action: "flag"
```

### Default Rules by Category

#### Rate Limiting (Firewall)
| Rule | Threshold | Severity | Action |
|------|-----------|----------|--------|
| High request rate | 60/min | critical | block |
| Warning rate | 30/min | warning | flag |
| High request count | 500 requests | critical | block |
| Long session | 1 hour | critical | block |
| Large data transfer | 50MB | critical | block |

#### Prompt Injection (OWASP LLM01)
| Pattern | Severity | Action |
|---------|----------|--------|
| "ignore previous instructions" | critical | block |
| "jailbreak mode" | critical | terminate |
| "you are now DAN" | critical | terminate |
| System prompt tags | critical | block |

#### Supply Chain (OWASP LLM05)
| Control | Config | Effect |
|---------|--------|--------|
| Strict model matching | `strict_model_matching: true` | Reject unknown models |
| Model blocklist | `blocked_models: ["gpt-4-*"]` | Block specific models |
| Backend allowlist | `backends:` config | Only configured backends |

#### Output Handling (OWASP LLM02)
| Pattern | Severity | Action |
|---------|----------|--------|
| `<script>` tags | warning | flag |
| SQL in response | warning | flag |
| Shell commands in response | warning | flag |
| Unsafe deserialization | critical | flag |

#### Tool/Plugin Use (OWASP LLM07)
| Pattern | Severity | Action |
|---------|----------|--------|
| File system access | warning | flag |
| Code execution | critical | flag |
| Network access (suspicious) | warning | flag |
| Credential access | critical | block |

#### Sensitive Data (OWASP LLM06)
| Pattern | Severity | Action |
|---------|----------|--------|
| SSN format | warning | flag |
| Credit card | warning | flag |
| API key request | warning | flag |
| Bulk data extraction | warning | flag |

#### Excessive Agency (OWASP LLM08)
| Pattern | Severity | Action |
|---------|----------|--------|
| Shell execution | critical | block |
| rm -rf | critical | terminate |
| sudo/root | critical | block |
| curl pipe to sh | critical | terminate |
| SQL injection | critical | terminate |

#### Overreliance (OWASP LLM09)
| Pattern | Severity | Action |
|---------|----------|--------|
| Medical advice request | warning | flag |
| Legal advice request | warning | flag |
| Financial advice request | warning | flag |
| Low-confidence hedging | info | flag |
| Uncertainty indicators | info | flag |

#### Model Theft (OWASP LLM10)
| Pattern | Severity | Action |
|---------|----------|--------|
| Architecture probing | warning | flag |
| Training data extraction | warning | flag |
| Model replication request | warning | flag |
| Systematic probing | warning | flag |

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
make run-redis          # Run with Redis session store

# Testing
make test               # Run unit tests (fast, no dependencies)
make test-integration   # Run integration tests (requires Redis)
make test-all           # Run all tests
make test-coverage      # Unit test coverage report
go test -v ./test/unit -run TestSessionKill  # Run a single test

# Redis management
make redis-up           # Start Redis container
make redis-down         # Stop Redis container
make redis-keys         # View Redis keys
make redis-flush        # Clear Redis

# Telemetry / Tracing
make run-telemetry      # Run with stdout trace exporter (debugging)
make run-jaeger         # Run with Jaeger tracing
make jaeger-up          # Start Jaeger container
make jaeger-ui          # Open Jaeger UI in browser

# Storage / History
make run-storage        # Run with SQLite storage enabled
make run-full           # Run with all features (storage + telemetry)
make history            # View session history
make history-stats      # View historical statistics
make history-timeseries # View time series data

# Code quality
make fmt                # Format code
make lint               # Run golangci-lint (requires golangci-lint)

# Quick verification
make test-ollama        # Test basic proxy against Ollama
make test-stream        # Test streaming
make sessions           # View active sessions
make stats              # View stats

# Manual testing
curl http://localhost:8080/api/tags              # Test against Ollama
curl http://localhost:9090/control/sessions      # Check sessions
curl -X POST http://localhost:9090/control/sessions/{id}/kill  # Kill a session
```

## Test Coverage

Tests are in `test/` directory (black-box testing using only exported APIs):

```bash
make test              # Unit tests only (74 tests, fast)
make test-integration  # Integration tests (10 tests, requires Redis)
make test-all          # All tests (84 tests)
```

| Directory | File | Tests |
|-----------|------|-------|
| `test/unit/` | `session_test.go` | Session lifecycle: New, Touch, AddBytes, Kill, SetState, Duration, IdleTime, Snapshot |
| `test/unit/` | `store_test.go` | MemoryStore: Put, Get, Delete, List, Count, ActiveFilter |
| `test/unit/` | `storage_test.go` | SQLiteStore: SaveAndGet, ListSessions, GetStats, GetNotFound, Cleanup |
| `test/unit/` | `manager_test.go` | Manager: GetOrCreate, GeneratesID, RejectsKilledSession, AllowsTimedOutSessionID, Kill, ListActive, Stats, KillBlock modes (duration/permanent/until_hour_change), GetOrCreateByClient |
| `test/unit/` | `proxy_test.go` | Proxy: BasicRequest, CustomSessionID, KilledSessionRejected, BackendError, BytesTracking, HeadersForwarded |
| `test/unit/` | `router_test.go` | Router: NewRouter, HeaderPriority, ModelMatching, PathRouting, DefaultFallback, SingleBackendRouter |
| `test/unit/` | `control_test.go` | Control API: Health, Stats, Sessions list/get, Kill, CORS |
| `test/integration/` | `redis_test.go` | RedisStore: CRUD, KillChannel, Metadata, KillPersistsAcrossRestart, KilledStateLoadsCorrectly |

**Key test scenarios:**
- Killed sessions reject new requests (returns 403 with JSON error)
- Killed sessions can be resumed (state returns to Active)
- Terminated sessions cannot be resumed (permanent block)
- Killed session state persists across restarts (Redis)
- Kill block modes: duration expires after time, permanent never expires, until_hour_change blocks same hour
- Client IP-based sessions: same IP gets same session ID, different IPs get different sessions
- Custom session IDs are honored
- Session bytes/requests are tracked
- Headers are forwarded to backend
- Multi-backend routing: X-Backend header takes priority over model matching
- Model pattern matching: `gpt-4` matches `gpt-*`, `claude-3-opus` matches `claude-*`
- Path-based routing: `/openai/*` routes to openai backend
- Default fallback: unknown models fall back to default backend

## Environment Variables

- `ELIDA_LISTEN` — Proxy listen address (default: `:8080`)
- `ELIDA_BACKEND` — Backend URL (default: `http://localhost:11434`)
- `ELIDA_CONTROL_LISTEN` — Control API address (default: `:9090`)
- `ELIDA_LOG_LEVEL` — Log level: debug, info, warn, error
- `ELIDA_SESSION_STORE` — Session store: `memory` (default) or `redis`
- `ELIDA_REDIS_ADDR` — Redis address (default: `localhost:6379`)
- `ELIDA_REDIS_PASSWORD` — Redis password (default: empty)
- `ELIDA_TLS_ENABLED` — Enable TLS/HTTPS (default: `false`)
- `ELIDA_TLS_CERT_FILE` — Path to TLS certificate file
- `ELIDA_TLS_KEY_FILE` — Path to TLS private key file
- `ELIDA_TLS_AUTO_CERT` — Generate self-signed certificate (default: `false`)
- `ELIDA_TELEMETRY_ENABLED` — Enable OpenTelemetry (default: `false`)
- `ELIDA_TELEMETRY_EXPORTER` — Exporter type: `otlp`, `stdout`, or `none`
- `ELIDA_TELEMETRY_ENDPOINT` — OTLP endpoint (default: `localhost:4317`)
- `OTEL_EXPORTER_OTLP_ENDPOINT` — Standard OTel env var (also enables telemetry)
- `ELIDA_STORAGE_ENABLED` — Enable SQLite storage for history (default: `false`)
- `ELIDA_STORAGE_PATH` — SQLite database path (default: `data/elida.db`)
- `ELIDA_STORAGE_CAPTURE_MODE` — Capture mode: `all` (default) or `flagged_only`
- `ELIDA_POLICY_ENABLED` — Enable policy engine (default: `false`)
- `ELIDA_POLICY_MODE` — Policy mode: `enforce` (default) or `audit` (dry-run)
- `ELIDA_POLICY_CAPTURE` — Capture request content for flagged sessions (default: `true`)
- `ELIDA_POLICY_PRESET` — Policy preset: `minimal`, `standard`, or `strict`

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
