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

## Flagged Sessions

When a policy violation is detected:

1. The session is flagged with the violation details
2. If `capture_flagged` is enabled, the full request/response body is persisted immediately (crash-safe)
3. The session appears in the flagged sessions list

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
