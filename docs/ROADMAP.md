# ELIDA Roadmap

## Competitive Landscape (January 2026)

### Oso (osohq.com)
- Four pillars: Simulate, Enforce, Detect, Audit
- Risk scoring per tool call
- "Lethal Trifecta" detection (deletes + wide reads + payments)
- Per-user/per-agent permission scoping
- Rogue agent pattern detection

### Tailscale Aperture
- Dead simple onboarding (single env var: `ANTHROPIC_BASE_URL`)
- Token usage tracking (input, output, cached, reasoning)
- Cost calculation per request/session
- Works with Claude Code, Codex, Gemini CLI out of box

---

## ELIDA Differentiators (Keep)

| Feature | Status | Why Unique |
|---------|--------|------------|
| Session-aware proxy (SBC model) | ✅ Done | Only proxy with telecom-style session control |
| Kill/Resume/Terminate lifecycle | ✅ Done | Granular agent control, not just block |
| Voice/WebSocket support | ✅ Done | STT/TTS transcript capture |
| Self-hosted, no cloud | ✅ Done | Enterprise data sovereignty |
| Open source | ⚠️ TBD | License decision needed |

---

## Priority Gaps

### 1. Token & Cost Tracking

**Status:** Not implemented
**Priority:** High (table stakes)
**Effort:** Medium

#### Why
Both Oso and Aperture have this. Without it, ELIDA can't answer "how much did this session cost?"

#### Implementation

**Files to create:**
- `internal/tokens/parser.go` — Parse token usage from LLM responses

**Files to modify:**
- `internal/session/session.go` — Add `TokensIn`, `TokensOut`, `EstimatedCost` fields
- `internal/proxy/proxy.go` — Call token parser after response
- `internal/config/config.go` — Add `CostConfig` for model pricing

**Token parsing logic:**
```go
type TokenUsage struct {
    InputTokens      int64
    OutputTokens     int64
    CachedTokens     int64  // Anthropic prompt caching
    ReasoningTokens  int64  // o1 models
}

func ParseFromResponse(body []byte, backendType string) *TokenUsage {
    // OpenAI: usage.prompt_tokens, usage.completion_tokens
    // Anthropic: usage.input_tokens, usage.output_tokens
    // Streaming: parse final frame for usage data
}
```

**Config:**
```yaml
cost:
  enabled: true
  models:
    "gpt-4o":
      input_per_1m: 2.50
      output_per_1m: 10.00
    "claude-3-5-sonnet*":
      input_per_1m: 3.00
      output_per_1m: 15.00
```

**API enhancement:**
```json
{
  "session_id": "client-abc123-openai",
  "tokens_in": 15420,
  "tokens_out": 8230,
  "estimated_cost_usd": 0.47
}
```

---

### 2. Simple Onboarding

**Status:** Not implemented
**Priority:** High (UX gap)
**Effort:** Low

#### Why
Aperture's single env var (`ANTHROPIC_BASE_URL=http://localhost:8080`) is compelling. ELIDA requires a config file.

#### Implementation

**Modify:** `internal/config/config.go`

```go
func (c *Config) applyEnvOverrides() {
    // Auto-configure from BASE_URL patterns (Aperture-style)
    if url := os.Getenv("ANTHROPIC_BASE_URL"); url != "" {
        c.autoConfigureBackend("anthropic", "https://api.anthropic.com", "anthropic")
        // ELIDA becomes the proxy at the URL the client thinks is Anthropic
    }
    if url := os.Getenv("OPENAI_BASE_URL"); url != "" {
        c.autoConfigureBackend("openai", "https://api.openai.com", "openai")
    }
}
```

**Zero-config usage:**
```bash
# Start ELIDA
./bin/elida

# Point Claude Code at ELIDA
export ANTHROPIC_BASE_URL=http://localhost:8080
claude
```

---

### 3. Prometheus Metrics

**Status:** Not implemented
**Priority:** Medium (enterprise observability)
**Effort:** Medium

#### Why
Standard for enterprise observability stacks. Enables Grafana dashboards, alerting.

#### Implementation

**Files to create:**
- `internal/metrics/prometheus.go`

**Metrics to expose:**
```
elida_requests_total{backend,status}
elida_tokens_total{backend,direction}
elida_session_duration_seconds{backend,state}
elida_policy_violations_total{rule,severity,action}
elida_active_sessions{backend}
elida_websocket_connections{protocol}
```

**Config:**
```yaml
metrics:
  enabled: true
  path: "/metrics"  # Served on control port (9090)
```

**Dependency:**
```bash
go get github.com/prometheus/client_golang/prometheus
go get github.com/prometheus/client_golang/prometheus/promhttp
```

---

### 4. Webhook Alerts

**Status:** Not implemented
**Priority:** Medium (SIEM integration)
**Effort:** Medium

#### Why
Real-time alerting when policies trigger. Integration with Slack, PagerDuty, SIEM.

#### Implementation

**Files to create:**
- `internal/webhooks/webhook.go`

**Trigger point:** `internal/policy/policy.go` → `recordViolations()`

```go
func (e *Engine) recordViolations(sessionID string, violations []Violation) {
    // ... existing code

    if e.webhook != nil && e.webhook.Enabled {
        event := ViolationEvent{
            Timestamp:  time.Now(),
            SessionID:  sessionID,
            Violations: violations,
        }
        go e.webhook.Send(event)
    }
}
```

**Config:**
```yaml
policy:
  webhook:
    enabled: true
    url: "https://hooks.slack.com/services/..."
    min_severity: "warning"
    async: true
    timeout: 5s
    retry_attempts: 3
```

**Payload:**
```json
{
  "timestamp": "2026-01-28T15:30:00Z",
  "event_type": "policy_violation",
  "session_id": "client-abc123-openai",
  "violations": [
    {
      "rule_name": "prompt_injection_ignore",
      "severity": "critical",
      "action": "block"
    }
  ]
}
```

---

## Future Features (Lower Priority)

### From Oso
- [ ] Red-team simulation mode
- [ ] Risk scoring per tool call
- [ ] "Lethal Trifecta" detection
- [ ] Per-user/per-agent permission scoping

### From Aperture
- [ ] S3 export for SIEM
- [ ] Tool call breakdown by type
- [ ] Session timeline visualization

### Agent-to-Agent
- [ ] Recursive call tracking
- [ ] Call depth limits
- [ ] Parent-child session linking
- [ ] Distributed trace IDs

---

## Voice/Audio Enhancements

### Currently Implemented
- [x] WebSocket proxy (OpenAI Realtime, Deepgram, ElevenLabs, LiveKit)
- [x] Transcript capture (STT/TTS)
- [x] Voice session lifecycle (INVITE/BYE/Hold/Resume)
- [x] Post-session policy scanning
- [x] **Voice session CDR persistence** (transcripts saved to SQLite)
- [x] **REST TTS tracking** (OpenAI TTS, Deepgram Aura, ElevenLabs)

### Voice CDR API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/control/voice` | GET | List **active** voice sessions |
| `/control/voice-history` | GET | List **persisted** voice session CDRs |
| `/control/voice-history/stats` | GET | Aggregate voice session statistics |
| `/control/voice-history/{id}` | GET | Get voice session with full transcript |
| `/control/tts` | GET | List TTS requests |
| `/control/tts/stats` | GET | Aggregate TTS statistics |

### Example Voice CDR
```json
{
  "id": "5b97e226",
  "parent_session_id": "client-abc123-mock",
  "model": "gpt-4o-realtime",
  "protocol": "openai_realtime",
  "duration_ms": 2500,
  "transcript": [
    {"speaker": "user", "text": "What is AI?", "source": "stt"},
    {"speaker": "assistant", "text": "AI is...", "source": "stt"}
  ]
}
```

### Example TTS Record
```json
{
  "id": "f23a9e09",
  "session_id": "client-abc123-openai",
  "provider": "openai",
  "model": "tts-1",
  "voice": "nova",
  "text": "Hello, how can I help?",
  "text_length": 22
}
```

### Future Enhancements
- [ ] Real-time speech analytics (sentiment, coaching)
- [ ] ASR confidence scoring
- [ ] Speaker diarization tracking
- [ ] Interruption/barge-in detection

---

## Implementation Priority

| Feature | Effort | Impact | Status |
|---------|--------|--------|--------|
| Voice Transcript Persistence (CDR) | Medium | **Critical** | ✅ **Done** |
| REST TTS Tracking | Low | High | ✅ **Done** |
| Token/Cost Tracking | Medium | High | **Next** |
| Simple Onboarding | Low | High | Planned |
| Prometheus Metrics | Medium | Medium | Planned |
| Webhook Alerts | Medium | Medium | Planned |

---

## References

- https://tailscale.com/blog/aperture-private-alpha
- https://www.osohq.com/
- https://www.osohq.com/developers/ai-agents-gone-rogue
- https://www.osohq.com/docs/oso-for-agents/overview
