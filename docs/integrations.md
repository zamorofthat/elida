# Integrations

ELIDA complements your existing AI infrastructure. It doesn't replace your gateway, or SIEM orchestrator — it adds the session governance layer between them.

---

## LiteLLM

LiteLLM provides a unified API across 100+ LLM providers. ELIDA sits in front of LiteLLM to add session tracking and policy enforcement.

```
AI Agent → ELIDA (session/policy) → LiteLLM (provider routing) → OpenAI / Anthropic / etc.
```

### Setup

```yaml
# elida.yaml
backend: "http://localhost:4000"  # LiteLLM proxy
```

```bash
# Start LiteLLM
litellm --model gpt-4 --port 4000

# Start ELIDA in front of it
docker run -p 8080:8080 -p 9090:9090 \
  -e ELIDA_BACKEND=http://host.docker.internal:4000 \
  ghcr.io/zamorofthat/elida:latest

# Point your agent at ELIDA
OPENAI_BASE_URL=http://localhost:8080 your-agent
```

### What each does

| Capability | LiteLLM | ELIDA |
|-----------|---------|-------|
| Multi-provider routing | Yes | Yes |
| Request-level load balancing | Yes | No |
| Session tracking | No | Yes |
| Kill switch | No | Yes |
| Policy enforcement | No | Yes |
| Risk scoring | No | Yes |
| CDR audit trail | No | Yes |

---

## Portkey

Portkey is an AI gateway with logging, caching, and request-level controls. ELIDA adds session-level governance on top.

```
AI Agent → ELIDA (session/policy) → Portkey (gateway) → Providers
```

### Setup

```yaml
# elida.yaml
backend: "https://api.portkey.ai"
```

```bash
# Point ELIDA at Portkey
docker run -p 8080:8080 -p 9090:9090 \
  -e ELIDA_BACKEND=https://api.portkey.ai \
  ghcr.io/zamorofthat/elida:latest

# Agent talks to ELIDA, which proxies through Portkey
OPENAI_BASE_URL=http://localhost:8080 your-agent
```

### What each does

| Capability | Portkey | ELIDA |
|-----------|---------|-------|
| Request logging | Yes | Yes (session-scoped) |
| Caching | Yes | No |
| Prompt management | Yes | No |
| Session tracking | No | Yes |
| Kill switch | No | Yes |
| Real-time intervention | No | Yes |
| Risk ladder | No | Yes |
| Wire-level inspection | No | Yes |

---

## Tailscale Aperture

Aperture provides identity-aware access control via Tailscale. ELIDA adds behavioral enforcement on top of identity.

```
AI Agent → Tailscale → Aperture (identity/authz) → ELIDA (session/policy) → Provider
```

### Setup

```yaml
# elida.yaml — use Tailscale identity as session key
session:
  header: "X-Tailscale-User"
  generate_if_missing: false
```

Configure Aperture to forward traffic to ELIDA, which then proxies to the AI provider. Every ELIDA session is now tied to a Tailscale identity.

### What each does

| Capability | Aperture | ELIDA |
|-----------|----------|-------|
| Network identity | Yes (Tailscale) | No |
| ACLs (who can connect) | Yes | No |
| Session tracking | No | Yes |
| What the agent is doing | No | Yes (wire inspection) |
| Kill switch | No | Yes |
| CDR with identity | No | Yes (via identity headers) |

### Combined value

Neither tool alone provides both identity and behavioral enforcement:

- Aperture knows **who** the agent is, not **what** it's doing
- ELIDA knows **what** the agent is doing, not **who** it is
- Together: "agent `deploy-bot@prod-machine` sent `rm -rf /` — session killed, CDR with full identity attached"

---

## Oso

Oso provides agent authorization and monitoring via Tailscale. ELIDA provides wire-level session governance without requiring agent cooperation.

```
                  ┌─── Oso (agent self-reports via tailnet)
AI Agent ─────────┤
                  └─── ELIDA (inspects wire traffic) ──→ Provider
```

### Key difference

| Aspect | Oso | ELIDA |
|--------|-----|-------|
| Trust model | Agent self-reports | Zero-trust wire inspection |
| Infrastructure | Requires Tailscale tailnet | HTTP proxy (no dependencies) |
| Visibility | What agent says it did | What agent actually did |
| Intervention | Post-hoc alerts | Real-time kill/block/throttle |
| Setup | SDK + tailnet | `ANTHROPIC_BASE_URL=http://elida` |

ELIDA doesn't require agent cooperation. It sits on the wire — the agent can't lie, omit, or refuse to report. If it makes an API call, ELIDA sees it.

---

## OpenTelemetry / SIEM

ELIDA exports structured telemetry via OTLP to any compatible collector or SIEM.

```
ELIDA → OTEL Collector → Cribl / Datadog / Elastic / Grafana / Splunk
```

### Setup

```yaml
# elida.yaml
telemetry:
  enabled: true
  exporter: "otlp"
  endpoint: "your-collector:4317"
  capture_content: "flagged"  # "none", "flagged", or "all"
```

### Capture modes

| Mode | What gets sent | Use case |
|------|---------------|----------|
| `"none"` | Metadata only (no bodies) | Privacy-first, default |
| `"flagged"` | Only flagged session content | Security monitoring |
| `"all"` | Every request/response body | Full compliance audit |

### Violation fields for SIEM correlation

Every violation includes structured fields for automated SIEM rules:

```json
{
  "rule": "prompt_injection_ignore_request",
  "severity": "critical",
  "effective_severity": "warning",
  "source_role": "assistant",
  "message_index": 63,
  "event_category": "prompt_injection",
  "framework_ref": "OWASP-LLM01"
}
```

---

## Claude Code / Cursor / AI IDEs

Any AI-powered development tool that uses the Anthropic or OpenAI API can be routed through ELIDA.

### Claude Code

```bash
ANTHROPIC_BASE_URL=http://localhost:8080 claude
```

### Cursor

```bash
OPENAI_BASE_URL=http://localhost:8080 cursor
```

### VS Code + Continue

Set the base URL in Continue's config to point at ELIDA.

### What you get

- Every coding session tracked with full request/response capture
- Policy enforcement on tool calls (block dangerous Bash commands)
- Kill switch if an agent starts deleting files
- Audit trail of everything the AI did in your codebase
