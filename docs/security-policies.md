# Security Policies

ELIDA includes a policy engine with 40+ built-in rules covering the OWASP LLM Top 10.

## Enabling the Policy Engine

```yaml
policy:
  enabled: true
  mode: "enforce"       # "enforce" blocks violations, "audit" logs only
  preset: "standard"    # "minimal", "standard", or "strict"
  capture_flagged: true # Capture full request/response for flagged sessions
```

Or via environment variables:

```bash
ELIDA_POLICY_ENABLED=true
ELIDA_POLICY_MODE=enforce
ELIDA_POLICY_PRESET=standard
```

## Modes

| Mode | Behavior |
|------|----------|
| `enforce` | Blocks requests that violate policy. Blocked requests return immediately (~49ms, no backend call). |
| `audit` | Logs violations but allows all requests through. Use this to evaluate rules before enforcing. |

## Presets

| Preset | Description |
|--------|-------------|
| `minimal` | Basic protections — high request counts, obvious prompt injection |
| `standard` | Balanced coverage of OWASP LLM Top 10 |
| `strict` | Full rule set with lower thresholds |

## OWASP LLM Top 10 Coverage

| OWASP ID | Category | What ELIDA detects |
|----------|----------|-------------------|
| LLM01 | Prompt Injection | Injection patterns in request content |
| LLM02 | Insecure Output | XSS, SQL injection, shell command patterns in responses |
| LLM06 | Sensitive Info Disclosure | PII and credential patterns in requests and responses |
| LLM07 | Insecure Plugin Design | Suspicious tool/plugin invocations |
| LLM08 | Excessive Agency | Agents exceeding expected behavior boundaries |
| LLM10 | Model Theft | Patterns indicating model extraction attempts |

## Custom Rules

Add custom rules alongside presets:

```yaml
policy:
  enabled: true
  preset: "standard"
  rules:
    - name: "high_request_count"
      type: "request_count"
      threshold: 100
      severity: "warning"
```

## Scanning Modes

ELIDA supports two scanning approaches for streaming responses:

- **Chunked** (default) — Scans chunks as they arrive. Lower latency, may miss patterns split across chunks.
- **Buffered** — Buffers the full response before scanning. Higher latency, catches everything.

## Per-Message Scanning & Source Attribution

ELIDA scans each message in the conversation individually rather than concatenating all content. This provides precise attribution for every violation:

| Field | Description | Example |
|-------|-------------|---------|
| `source_role` | Which message role triggered the violation | `user`, `assistant`, `system`, `tool` |
| `message_index` | Position in the messages array | `3`, `63`, `-1` (top-level system) |
| `source_content` | Full message content (truncated to max_capture_size) | The text that contained the match |
| `effective_severity` | Severity after source-role weighting | `warning` (from critical + assistant source) |
| `event_category` | SIEM classification category | `prompt_injection`, `data_exfil`, `rate_limit` |
| `framework_ref` | Security framework reference | `OWASP-LLM01`, `ELIDA-FIREWALL` |

### System Prompt Caching

System prompts (both Anthropic top-level `system` field and OpenAI-style `role: "system"` messages) are hash-cached per session. The system prompt is scanned on the first request, and subsequent requests skip scanning if the hash matches. If the system prompt changes mid-session, ELIDA logs a warning and re-scans.

### SIEM Integration

Violation logs are structured for direct SIEM consumption:

```json
{
  "msg": "content policy violation detected",
  "rule": "prompt_injection_ignore_request",
  "severity": "critical",
  "effective_severity": "warning",
  "source_role": "assistant",
  "message_index": 63,
  "event_category": "prompt_injection",
  "framework_ref": "OWASP-LLM01",
  "source_content": "...the message content that triggered the match...",
  "matched": "ignore all previous instructions"
}
```

## Flagged Sessions

When a policy violation is detected:

1. The session is flagged with the violation details
2. If `capture_flagged` is enabled, the full request/response body is persisted immediately (crash-safe)
3. The session appears in the flagged sessions list

> **Note**: The dashboard and API show `effective_severity` (source-weighted) instead of raw rule severity. A critical rule triggered by an assistant message echoing safety instructions displays as `WARNING` with an `ASSISTANT` source badge for context.

```bash
# View flagged sessions
curl http://localhost:9090/control/flagged
```

## Capture-All Mode

For full audit/compliance, capture every request/response — not just policy-flagged ones:

```yaml
storage:
  enabled: true
  capture_mode: "all"
  max_capture_size: 10000         # 10KB per body
  max_captured_per_session: 100   # Max pairs per session
```

```bash
make run-demo
# Or:
ELIDA_STORAGE_ENABLED=true ELIDA_STORAGE_CAPTURE_MODE=all ./bin/elida
```
