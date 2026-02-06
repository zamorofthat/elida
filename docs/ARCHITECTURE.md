# ELIDA Architecture Document

**Edge Layer for Intelligent Defense of Agents**

Version: 1.0
Date: February 2026
Author: Aaron Zamora

---

## Executive Summary

ELIDA is a session-aware reverse proxy specifically designed for AI client traffic—including both autonomous AI agents and human users interacting with AI model-powered applications. It introduces the concept of **Session Border Control (SBC) for AI**, borrowing architectural patterns from telecommunications to provide enterprise-grade visibility, control, and security for AI deployments.

Unlike traditional API gateways that treat each request independently, ELIDA maintains stateful session context across multiple requests from the same client, enabling real-time threat detection, behavioral analysis, and instant session termination capabilities.

---

## Problem Statement

### The AI Client Control Gap

As enterprises deploy AI clients—including both autonomous AI agents and human users interacting with AI model applications—they face critical challenges:

1. **No Session Visibility**: Traditional proxies treat each AI model API call independently, losing the conversational context that defines agent behavior.

2. **Inability to Stop Runaway Agents**: Without session tracking, there is no mechanism to immediately halt a misbehaving agent mid-conversation.

3. **Security Blind Spots**: Prompt injection attacks, data exfiltration, and excessive agency often span multiple requests within a session—invisible to stateless monitoring.

4. **Compliance Gaps**: Regulations require audit trails of AI interactions, but request-level logging misses the session-level patterns that matter.

5. **Voice AI Complexity**: Real-time voice agents using WebSocket connections require specialized handling that HTTP-only gateways cannot provide.

### Existing Solutions Fall Short

| Solution Type | Limitation |
|---------------|------------|
| API Gateways (Kong, Apigee) | Request-level only, no session state |
| Web Application Firewalls | Pattern matching without AI model context |
| AI Observability Tools | Post-hoc analysis, cannot intervene |
| Cloud Provider Controls | Vendor lock-in, limited customization |

---

## Core Innovation: Session Border Control for AI

ELIDA adapts the **Session Border Controller (SBC)** architecture from VoIP/telecommunications to AI agent traffic. In telecom, SBCs sit at network boundaries to manage voice sessions, enforce policies, and provide security. ELIDA applies this proven pattern to AI:

```
┌─────────────────────────────────────────────────────────────────┐
│                    TELECOM ANALOGY                               │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│   VoIP Phone ──► SBC ──► Carrier Network                        │
│       │          │                                               │
│       │          ├── Session tracking (call state)              │
│       │          ├── Policy enforcement (call routing)          │
│       │          ├── Security (fraud detection)                 │
│       │          └── CDR generation (billing/audit)             │
│                                                                  │
├─────────────────────────────────────────────────────────────────┤
│                    ELIDA IMPLEMENTATION                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│   AI Client ──► ELIDA ──► AI Model Providers                    │
│   (Agent or                                                      │
│    User App)                                                     │
│       │          │                                               │
│       │          ├── Session tracking (agent state)             │
│       │          ├── Policy enforcement (security rules)        │
│       │          ├── Security (prompt injection detection)      │
│       │          └── SDR generation (session detail records)    │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Key Architectural Principles

1. **Session-Centric Model**: All operations are organized around sessions, not individual requests.

2. **Real-Time Intervention**: Active sessions can be killed, paused, or modified instantly.

3. **Protocol Agnosticism**: Supports HTTP (REST APIs), Server-Sent Events (streaming), and WebSocket (voice/real-time).

4. **Multi-Backend Routing**: Single proxy handles routing to multiple AI model providers based on model, path, or header.

5. **Defense in Depth**: Multiple policy layers (firewall rules, content inspection, behavioral analysis).

---

## System Architecture

### High-Level Component Diagram

```
                                    ┌─────────────────────────────────────┐
                                    │         ELIDA PROXY                 │
                                    │                                     │
┌──────────┐                        │  ┌─────────────────────────────┐   │                    ┌──────────────┐
│          │   HTTP/WS Requests     │  │      Session Manager        │   │                    │    AI Model  │
│    AI    │ ──────────────────────►│  │  ┌─────────────────────┐   │   │   Proxied Requests │   Providers  │
│ Clients  │                        │  │  │ Session Store       │   │   │ ──────────────────►│  (OpenAI,    │
│          │◄──────────────────────┐│  │  │ (Memory/Redis)      │   │   │                    │   Anthropic, │
└──────────┘   Responses           ││  │  └─────────────────────┘   │   │◄───────────────────│   Mistral)   │
                                   ││  │                             │   │   Responses        │              │
                                   ││  │  ┌─────────────────────┐   │   │                    │              │
                                   ││  │  │ Kill Switch         │   │   │                    └──────────────┘
                                   ││  │  │ (Block/Resume)      │   │   │
                                   ││  │  └─────────────────────┘   │   │
                                   ││  └─────────────────────────────┘   │
                                   ││                                     │
                                   ││  ┌─────────────────────────────┐   │
                                   ││  │      Policy Engine          │   │
                                   ││  │  ┌─────────────────────┐   │   │
                                   ││  │  │ Rule Evaluator      │   │   │
                                   ││  │  │ (Rate/Content/      │   │   │
                                   ││  │  │  Behavioral)        │   │   │
                                   ││  │  └─────────────────────┘   │   │
                                   ││  │                             │   │
                                   ││  │  ┌─────────────────────┐   │   │
                                   ││  │  │ Capture Buffer      │   │   │
                                   ││  │  │ (Request/Response)  │   │   │
                                   ││  │  └─────────────────────┘   │   │
                                   ││  └─────────────────────────────┘   │
                                   ││                                     │
                                   ││  ┌─────────────────────────────┐   │
                                   │└──│      Proxy Handler          │   │
                                   │   │  ┌─────────────────────┐   │   │
                                   │   │  │ HTTP Proxy          │   │   │
                                   │   │  │ (Direct/Chunked/    │   │   │
                                   │   │  │  SSE Streaming)     │   │   │
                                   │   │  └─────────────────────┘   │   │
                                   │   │                             │   │
                                   │   │  ┌─────────────────────┐   │   │
                                   │   │  │ WebSocket Proxy     │   │   │
                                   │   │  │ (Voice Sessions)    │   │   │
                                   │   │  └─────────────────────┘   │   │
                                   │   └─────────────────────────────┘   │
                                   │                                     │
                                   │   ┌─────────────────────────────┐   │
                                   │   │      Multi-Backend Router   │   │
                                   │   │  • Header-based routing     │   │
                                   │   │  • Model pattern matching   │   │
                                   │   │  • Path prefix routing      │   │
                                   │   │  • Default fallback         │   │
                                   │   └─────────────────────────────┘   │
                                   │                                     │
                                   └─────────────────────────────────────┘
                                                     │
                    ┌────────────────────────────────┼────────────────────────────────┐
                    │                                │                                │
                    ▼                                ▼                                ▼
           ┌──────────────┐                ┌──────────────────┐            ┌──────────────────┐
           │   Control    │                │    Telemetry     │            │    Storage       │
           │     API      │                │   (OpenTelemetry)│            │    (SQLite)      │
           │              │                │                  │            │                  │
           │ • Sessions   │                │ • Traces         │            │ • Session History│
           │ • Kill/Resume│                │ • Session Records│            │ • Voice CDRs     │
           │ • Flagged    │                │ • Violations     │            │ • TTS Requests   │
           │ • History    │                │ • Captures       │            │ • Flagged Content│
           │ • Voice      │                │                  │            │                  │
           └──────────────┘                └──────────────────┘            └──────────────────┘
```

---

## Core Components

### 1. Session Manager

The Session Manager is the central coordination point for all session lifecycle operations.

#### Session Identification

Sessions are identified using a composite key:

```
Session ID = hash(Client IP) + Backend Name
Format: client-{hash}-{backendName}
Example: client-e8eacf29-anthropic
```

This design enables:
- **Per-backend isolation**: Kill an agent's Anthropic sessions without affecting OpenAI sessions
- **Client tracking**: Correlate requests from the same source
- **Horizontal scaling**: Deterministic session assignment across proxy instances

#### Session States

```
┌─────────┐     timeout      ┌──────────┐
│  Active │ ───────────────► │ TimedOut │
└────┬────┘                  └──────────┘
     │
     │ kill()
     ▼
┌─────────┐     resume()     ┌─────────┐
│ Killed  │ ───────────────► │ Active  │
└────┬────┘                  └─────────┘
     │
     │ timeout / terminate()
     ▼
┌────────────┐
│ Terminated │ (permanent, cannot resume)
└────────────┘
```

#### Session Metrics Tracked

| Metric | Description | Use Case |
|--------|-------------|----------|
| `request_count` | Total requests in session | Rate limiting, anomaly detection |
| `bytes_in` | Request body bytes | Data exfiltration detection |
| `bytes_out` | Response body bytes | Cost tracking, abuse detection |
| `duration` | Session lifetime | Long-running agent detection |
| `idle_time` | Time since last request | Session timeout decisions |
| `backends_used` | Map of backend → request count | Multi-model tracking |

#### Kill Switch Mechanism

The kill switch provides immediate session termination with configurable blocking:

**Block Modes:**
1. **Duration**: Block for specified time (e.g., 30 minutes)
2. **Until Hour Change**: Block until the hour changes (session IDs regenerate hourly)
3. **Permanent**: Block until server restart

**Implementation:**
```go
type Session struct {
    killChan   chan struct{}  // Closed when session is killed
    Killed     bool           // Kill state flag
    Terminated bool           // Permanent termination flag
}

// Active requests check killChan for immediate abort
select {
case <-s.killChan:
    return ErrSessionKilled
case <-responseComplete:
    return nil
}
```

### 2. Policy Engine

The Policy Engine implements a rule-based security framework aligned with OWASP LLM Top 10 and NIST AI RMF.

#### Rule Types

| Type | Evaluation Target | Example |
|------|-------------------|---------|
| `metric` | Session metrics | `request_count > 500` |
| `content_match` | Request/response body | Regex pattern matching |
| `rate` | Requests per time window | `60 requests/minute` |

#### Rule Actions

| Action | Behavior | Use Case |
|--------|----------|----------|
| `flag` | Log violation, allow request | Monitoring, audit mode |
| `block` | Reject request with 403 | Active protection |
| `terminate` | Kill session immediately | Critical threats |

#### Content Inspection Pipeline

```
Request Body
     │
     ▼
┌─────────────────────────────────────────────────────────┐
│                  CONTENT INSPECTION                      │
│                                                          │
│  1. Prompt Injection Detection (LLM01)                  │
│     • "ignore previous instructions"                     │
│     • "you are now DAN"                                  │
│     • System prompt tag injection                        │
│                                                          │
│  2. Sensitive Data Detection (LLM06)                    │
│     • PII patterns (SSN, credit cards)                  │
│     • API key patterns                                   │
│     • Internal information markers                       │
│                                                          │
│  3. Excessive Agency Detection (LLM08)                  │
│     • Shell command patterns                             │
│     • File system access                                 │
│     • Privilege escalation attempts                      │
│                                                          │
│  4. Model Theft Detection (LLM10)                       │
│     • Architecture probing                               │
│     • Training data extraction                           │
│                                                          │
└─────────────────────────────────────────────────────────┘
     │
     ▼
Response Body (LLM02 - Insecure Output Handling)
     │
     ▼
┌─────────────────────────────────────────────────────────┐
│  • XSS/Script injection in responses                    │
│  • SQL statements in output                              │
│  • Shell commands in output                              │
│  • Unsafe deserialization patterns                       │
└─────────────────────────────────────────────────────────┘
```

#### Audit vs Enforce Mode

| Mode | Violation Behavior | Use Case |
|------|-------------------|----------|
| `enforce` | Actions executed (block/terminate) | Production protection |
| `audit` | Actions logged only, requests forwarded | Policy testing, tuning |

### 3. Multi-Backend Router

The router enables a single ELIDA instance to proxy to multiple AI model providers with intelligent routing.

#### Routing Priority

```
1. X-Backend Header    →  Explicit backend selection
         │
         ▼ (not found)
2. Model Matching      →  Parse request body, match model patterns
         │                 e.g., "gpt-*" → openai, "claude-*" → anthropic
         ▼ (no match)
3. Path Prefix         →  URL path routing
         │                 e.g., "/openai/*" → openai backend
         ▼ (no match)
4. Default Backend     →  Fallback to configured default
```

#### Backend Configuration

```yaml
backends:
  anthropic:
    url: "https://api.anthropic.com"
    type: anthropic
    models: ["claude-*"]
    default: true

  openai:
    url: "https://api.openai.com"
    type: openai
    models: ["gpt-*", "o1-*"]

  mistral:
    url: "https://api.mistral.ai"
    type: mistral
    models: ["mistral-*", "codestral-*"]
```

#### Per-Backend Connection Pooling

Each backend maintains its own HTTP transport for:
- Independent connection limits
- Separate timeout configurations
- Isolated failure domains

### 4. WebSocket Proxy & Voice Session Manager

ELIDA provides specialized handling for WebSocket connections used by voice AI agents.

#### Voice Session Model (SIP-Inspired)

```
WebSocket Connection
        │
        │  session.create (INVITE)
        ▼
┌───────────────┐
│    INVITING   │
└───────┬───────┘
        │  session.created (200 OK)
        ▼
┌───────────────┐
│    ACTIVE     │◄────────────────┐
└───────┬───────┘                 │
        │                         │
   ┌────┴────┐                    │
   │         │                    │
   ▼         ▼                    │
┌──────┐  ┌──────┐    resume()   │
│ HOLD │  │ BYE  │ ──────────────┘
└──────┘  └──┬───┘
              │
              ▼
       ┌────────────┐
       │ TERMINATED │
       └────────────┘
```

#### Supported Voice Protocols

| Protocol | Provider | INVITE Signal | BYE Signal | Transcript Source |
|----------|----------|---------------|------------|-------------------|
| OpenAI Realtime | OpenAI | `session.create` | close/error | `response.audio_transcript.*` |
| Deepgram | Deepgram | `Metadata` | close | `Results.channel.alternatives` |
| ElevenLabs | ElevenLabs | `voice_settings` | `flush` | `text` field |
| LiveKit | LiveKit | `participant_joined` | `participant_left` | - |

#### Voice Session CDR (Call Detail Record)

Each voice session generates a CDR containing:

```json
{
  "id": "voice-a1b2c3d4",
  "parent_session_id": "client-abc123-openai",
  "state": "terminated",
  "protocol": "openai_realtime",
  "model": "gpt-4o-realtime",
  "voice": "alloy",
  "start_time": "2026-01-24T21:15:00Z",
  "end_time": "2026-01-24T21:20:00Z",
  "duration_ms": 300000,
  "turn_count": 15,
  "audio_duration_ms": 180000,
  "transcript": [
    {
      "timestamp": "2026-01-24T21:15:02Z",
      "speaker": "user",
      "text": "Hello, how are you?",
      "source": "stt"
    },
    {
      "timestamp": "2026-01-24T21:15:04Z",
      "speaker": "assistant",
      "text": "I'm doing well! How can I help?",
      "source": "stt"
    }
  ]
}
```

#### Two-Layer Policy Scanning for Voice

| Layer | When | Target | Actions |
|-------|------|--------|---------|
| Real-time | During connection | Text frames (JSON) | Block frame, terminate |
| Post-session | Voice session ends | Full transcript | Flag for review |

### 5. Capture System

ELIDA provides two independent capture mechanisms for forensic data collection.

#### Policy Captures (Flagged Sessions)

When policy violations occur, request/response content is captured:

```
Request with violation
        │
        ▼
┌─────────────────────────────────────┐
│  Policy Engine detects violation    │
│                                     │
│  Capture:                           │
│  • Request body (truncated)         │
│  • Response body (if flag action)   │
│  • Status code                       │
│  • Timestamp                         │
│  • Violation details                 │
└─────────────────────────────────────┘
        │
        ▼
  Persisted immediately to SQLite
  (survives crashes)
```

#### Capture-All Mode (Audit/Compliance)

For full audit requirements, every request/response is captured regardless of policy:

```yaml
storage:
  capture_mode: "all"           # vs "flagged_only"
  max_capture_size: 10000       # Truncate bodies > 10KB
  max_captured_per_session: 100 # Limit per session
```

**Implementation:**

```
                     Request
                        │
          ┌─────────────┴─────────────┐
          │                           │
          ▼                           ▼
   ┌──────────────┐           ┌──────────────┐
   │ Policy Engine│           │CaptureBuffer │
   │              │           │ (if all mode)│
   │ Flagged? ────┼──► Yes    │              │
   │              │           │ Always       │
   └──────────────┘           │ captures     │
                              └──────────────┘
                                      │
                        ┌─────────────┴─────────────┐
                        ▼                           ▼
                 Session End              Session End
                 (with violations)        (no violations)
                        │                           │
                        ▼                           ▼
                 Use policy              Use capture-all
                 captures                content
```

### 6. Telemetry System (OpenTelemetry)

ELIDA exports comprehensive telemetry for observability and audit.

#### Session Detail Record (SDR) Export

Each session generates an SDR exported via OpenTelemetry:

```
┌─────────────────────────────────────────────────────────────────┐
│                    session.record SPAN                          │
├─────────────────────────────────────────────────────────────────┤
│ Attributes:                                                      │
│   elida.session.id = "client-abc123-anthropic"                  │
│   elida.session.state = "completed"                              │
│   elida.backend = "anthropic"                                    │
│   elida.client.addr = "10.0.0.5:12345"                          │
│   elida.duration.ms = 45000                                      │
│   elida.request.count = 12                                       │
│   elida.bytes.in = 4500                                          │
│   elida.bytes.out = 128000                                       │
│   elida.violations.count = 2                                     │
│   elida.violations.rules = ["prompt_injection", "pii_detected"] │
│   elida.violations.max_severity = "critical"                    │
│   elida.captures.count = 2                                       │
├─────────────────────────────────────────────────────────────────┤
│ Events:                                                          │
│   policy.violation {                                             │
│     rule_name: "prompt_injection_ignore"                        │
│     severity: "critical"                                         │
│     matched_text: "ignore previous instructions"                │
│   }                                                              │
│   captured.request {                                             │
│     method: "POST"                                               │
│     path: "/v1/messages"                                         │
│     request_body: "{\"messages\":[...]}"                        │
│     response_body: "{\"content\":\"...\"}"                      │
│     status_code: 200                                             │
│   }                                                              │
└─────────────────────────────────────────────────────────────────┘
```

---

## Data Flows

### HTTP Request Flow

```
┌────────┐         ┌─────────────────────────────────────────────────────────────────┐         ┌─────────┐
│ Client │         │                           ELIDA                                  │         │ Backend │
└───┬────┘         └─────────────────────────────────────────────────────────────────┘         └────┬────┘
    │                                                                                                │
    │  1. HTTP Request                                                                               │
    │ ────────────────►                                                                              │
    │                   ┌─────────────────────────────────────────────────────────────┐             │
    │                   │ 2. Session Resolution                                        │             │
    │                   │    • Extract/generate session ID                             │             │
    │                   │    • Check if session exists                                 │             │
    │                   │    • Check if session is killed/blocked                      │             │
    │                   └─────────────────────────────────────────────────────────────┘             │
    │                                        │                                                       │
    │                   ┌────────────────────┴────────────────────┐                                 │
    │                   │ Blocked?                                │                                 │
    │                   └────────────────────┬────────────────────┘                                 │
    │                            ┌───────────┴───────────┐                                          │
    │                            ▼                       ▼                                          │
    │                    ┌──────────────┐        ┌──────────────┐                                   │
    │                    │ Return 403   │        │ Continue     │                                   │
    │ ◄──────────────────│              │        │              │                                   │
    │                    └──────────────┘        └──────┬───────┘                                   │
    │                                                   │                                           │
    │                   ┌───────────────────────────────┴───────────────────────────────┐          │
    │                   │ 3. Policy Evaluation (Pre-Request)                            │          │
    │                   │    • Rate limit check                                          │          │
    │                   │    • Content inspection (request body)                         │          │
    │                   │    • Behavioral analysis                                       │          │
    │                   └───────────────────────────────┬───────────────────────────────┘          │
    │                                                   │                                           │
    │                   ┌────────────────────┬──────────┴──────────┐                               │
    │                   ▼                    ▼                     ▼                               │
    │            ┌──────────┐         ┌──────────┐          ┌──────────┐                          │
    │            │  Block   │         │   Flag   │          │   Pass   │                          │
    │            │  Return  │         │  Capture │          │          │                          │
    │ ◄──────────│  403     │         │  Continue│          │          │                          │
    │            └──────────┘         └────┬─────┘          └────┬─────┘                          │
    │                                      │                     │                                 │
    │                                      └──────────┬──────────┘                                │
    │                                                 │                                            │
    │                   ┌─────────────────────────────┴─────────────────────────────────┐         │
    │                   │ 4. Backend Routing                                            │         │
    │                   │    • Header check (X-Backend)                                 │         │
    │                   │    • Model pattern matching                                    │         │
    │                   │    • Path prefix matching                                      │         │
    │                   │    • Default fallback                                          │         │
    │                   └─────────────────────────────┬─────────────────────────────────┘         │
    │                                                 │                                            │
    │                                                 │  5. Proxied Request                        │
    │                                                 │ ──────────────────────────────────────────►│
    │                                                 │                                            │
    │                                                 │  6. Backend Response                       │
    │                                                 │ ◄──────────────────────────────────────────│
    │                                                 │                                            │
    │                   ┌─────────────────────────────┴─────────────────────────────────┐         │
    │                   │ 7. Policy Evaluation (Post-Response)                          │         │
    │                   │    • Response content inspection (LLM02)                      │         │
    │                   │    • Capture response if flagged                              │         │
    │                   └─────────────────────────────┬─────────────────────────────────┘         │
    │                                                 │                                            │
    │                   ┌─────────────────────────────┴─────────────────────────────────┐         │
    │                   │ 8. Session Update                                             │         │
    │                   │    • Increment request count                                   │         │
    │                   │    • Add bytes in/out                                          │         │
    │                   │    • Update last activity time                                 │         │
    │                   │    • Record backend used                                       │         │
    │                   └─────────────────────────────┬─────────────────────────────────┘         │
    │                                                 │                                            │
    │  9. HTTP Response                               │                                            │
    │ ◄───────────────────────────────────────────────┘                                            │
    │                                                                                               │
```

### Session Kill Flow

```
┌──────────────┐         ┌─────────────────┐         ┌─────────────────┐
│   Operator   │         │  Control API    │         │ Session Manager │
└──────┬───────┘         └────────┬────────┘         └────────┬────────┘
       │                          │                           │
       │ POST /sessions/{id}/kill │                           │
       │ ────────────────────────►│                           │
       │                          │                           │
       │                          │ Kill(sessionID)           │
       │                          │ ──────────────────────────►│
       │                          │                           │
       │                          │                    ┌──────┴──────┐
       │                          │                    │ Close       │
       │                          │                    │ killChan    │
       │                          │                    │             │
       │                          │                    │ Set Killed  │
       │                          │                    │ = true      │
       │                          │                    │             │
       │                          │                    │ Export SDR  │
       │                          │                    │ immediately │
       │                          │                    │             │
       │                          │                    │ Add to      │
       │                          │                    │ blocklist   │
       │                          │                    └──────┬──────┘
       │                          │                           │
       │                          │ Session killed            │
       │                          │ ◄──────────────────────────│
       │                          │                           │
       │ {"status": "killed"}     │                           │
       │ ◄────────────────────────│                           │
       │                          │                           │
       │                          │                           │
       │                          │         ┌─────────────────┴─────────────────┐
       │                          │         │ Active requests on this session   │
       │                          │         │ receive killChan closure signal,  │
       │                          │         │ abort immediately with error      │
       │                          │         └───────────────────────────────────┘
       │                          │                           │
```

---

## Deployment Architecture

### Single Instance (Development/Small Scale)

```
┌─────────────────────────────────────────────────────────────┐
│                      Single ELIDA Instance                   │
│                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐       │
│  │ HTTP Proxy   │  │ Control API  │  │ Dashboard UI │       │
│  │ :8080        │  │ :9090        │  │ :9090        │       │
│  └──────────────┘  └──────────────┘  └──────────────┘       │
│                                                              │
│  ┌──────────────────────────────────────────────────┐       │
│  │               Memory Session Store                │       │
│  └──────────────────────────────────────────────────┘       │
│                                                              │
│  ┌──────────────────────────────────────────────────┐       │
│  │               SQLite Storage                      │       │
│  │               (data/elida.db)                     │       │
│  └──────────────────────────────────────────────────┘       │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

### Horizontally Scaled (Production)

```
                         ┌─────────────────────────────┐
                         │      Load Balancer          │
                         │   (sticky sessions or       │
                         │    session ID routing)      │
                         └────────────┬────────────────┘
                                      │
            ┌─────────────────────────┼─────────────────────────┐
            │                         │                         │
            ▼                         ▼                         ▼
   ┌─────────────────┐      ┌─────────────────┐      ┌─────────────────┐
   │   ELIDA #1      │      │   ELIDA #2      │      │   ELIDA #3      │
   │                 │      │                 │      │                 │
   │ Redis Session   │      │ Redis Session   │      │ Redis Session   │
   │ Store Client    │      │ Store Client    │      │ Store Client    │
   └────────┬────────┘      └────────┬────────┘      └────────┬────────┘
            │                        │                        │
            └────────────────────────┼────────────────────────┘
                                     │
                                     ▼
                         ┌─────────────────────────────┐
                         │         Redis               │
                         │   (Session State Store)     │
                         │                             │
                         │  • Session data (JSON)      │
                         │  • Kill signal pub/sub      │
                         │  • TTL-based expiry         │
                         └─────────────────────────────┘
                                     │
                                     │
            ┌────────────────────────┼────────────────────────┐
            │                        │                        │
            ▼                        ▼                        ▼
   ┌─────────────────┐      ┌─────────────────┐      ┌─────────────────┐
   │ SQLite (local)  │      │ SQLite (local)  │      │ SQLite (local)  │
   │ History/Flagged │      │ History/Flagged │      │ History/Flagged │
   └─────────────────┘      └─────────────────┘      └─────────────────┘
            │                        │                        │
            └────────────────────────┼────────────────────────┘
                                     │
                                     ▼
                         ┌─────────────────────────────┐
                         │    OpenTelemetry Collector  │
                         │         (Aggregation)       │
                         └─────────────────────────────┘
                                     │
                                     ▼
                         ┌─────────────────────────────┐
                         │    Jaeger / Datadog / etc   │
                         │     (Trace Storage)         │
                         └─────────────────────────────┘
```

### Redis Session Store Key Structure

```
elida:session:{session_id}     →  Session JSON (with TTL)
elida:sessions                 →  Set of active session IDs
elida:kill:{session_id}        →  Pub/sub channel for kill signals
elida:block:{client_hash}      →  Blocked client entries (with TTL)
```

---

## Security Considerations

### Defense in Depth Layers

```
┌─────────────────────────────────────────────────────────────────────────┐
│ Layer 1: Network                                                         │
│   • TLS termination (HTTPS/WSS)                                         │
│   • IP-based rate limiting (external LB)                                │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Layer 2: Session                                                         │
│   • Session-level rate limiting                                          │
│   • Behavioral anomaly detection                                         │
│   • Kill switch (immediate termination)                                  │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Layer 3: Content                                                         │
│   • Prompt injection detection                                           │
│   • PII/sensitive data scanning                                          │
│   • Excessive agency detection                                           │
│   • Response validation                                                  │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Layer 4: Audit                                                           │
│   • Full request/response capture (flagged or all)                      │
│   • Session Detail Records (SDR)                                         │
│   • Violation logging                                                    │
│   • OpenTelemetry export                                                 │
└─────────────────────────────────────────────────────────────────────────┘
```

### OWASP LLM Top 10 Coverage Matrix

| ID | Vulnerability | Detection Method | Action |
|----|--------------|------------------|--------|
| LLM01 | Prompt Injection | Regex patterns for jailbreak/override | Block/Terminate |
| LLM02 | Insecure Output | Response scanning for XSS/SQL/shell | Flag |
| LLM03 | Training Data Poisoning | N/A (training-time) | - |
| LLM04 | Model DoS | Rate limits, duration limits | Block |
| LLM05 | Supply Chain | Model allowlist/blocklist | Block |
| LLM06 | Sensitive Info | PII patterns, credential detection | Flag/Block |
| LLM07 | Insecure Plugin | Tool/function call monitoring | Flag |
| LLM08 | Excessive Agency | Shell, file, network access patterns | Block/Terminate |
| LLM09 | Overreliance | High-stakes domain flagging | Flag |
| LLM10 | Model Theft | Architecture probing detection | Flag |

---

## Novel Contributions

### 1. Session Border Control for AI Clients

ELIDA is the first system to apply SBC architecture to AI client traffic (both autonomous agents and user-facing applications), introducing:
- Session-centric security model for AI model API calls
- Real-time kill switch with configurable blocking modes
- Per-backend session isolation

### 2. Voice Session CDR for AI

Extending the telecom CDR concept to voice AI:
- SIP-inspired state machine (INVITE/BYE/HOLD)
- Full transcript capture and post-session analysis
- Protocol-agnostic voice session tracking

### 3. Dual-Mode Content Capture

Independent capture systems for different compliance needs:
- Policy-triggered capture for security forensics
- Capture-all mode for full audit compliance
- Configurable truncation and limits

### 4. Two-Layer Voice Policy Scanning

Separated real-time and post-session policy evaluation:
- Real-time: Text frame scanning without latency impact
- Post-session: Full transcript analysis for voice content

### 5. Session Detail Record (SDR) Export

Comprehensive session telemetry including:
- Full request/response body capture in trace events
- Violation details with matched patterns
- WebSocket frame statistics

---

## Glossary

| Term | Definition |
|------|------------|
| **SBC** | Session Border Controller - telecom device managing voice session boundaries |
| **SDR** | Session Detail Record - comprehensive record of a session's activity |
| **CDR** | Call Detail Record - telecom billing/audit record for voice calls |
| **INVITE** | SIP message initiating a voice session |
| **BYE** | SIP message terminating a voice session |
| **Kill Switch** | Immediate session termination mechanism |
| **Flagged Session** | Session with policy violations, marked for review |
| **Capture Buffer** | Memory buffer for policy-independent content capture |
| **Voice Session** | A single conversation within a WebSocket connection |

---

## References

1. RFC 3261 - SIP: Session Initiation Protocol
2. OWASP LLM Top 10 (2025)
3. NIST AI Risk Management Framework
4. OpenTelemetry Specification
5. WebSocket Protocol (RFC 6455)

---

*Document prepared for patent application purposes. All technical descriptions represent implemented functionality as of February 2026.*
