# Session Records (CDR-style Capture)

ELIDA captures detailed session records similar to Call Detail Records (CDRs) in telecommunications. When a session ends, all relevant data is persisted to SQLite for forensic analysis, compliance auditing, and operational visibility.

## Overview

Session records capture:
- **Session metadata**: ID, state, timing, request counts
- **Traffic metrics**: bytes in/out, backend used
- **Captured content**: full request/response bodies (when enabled)
- **Policy violations**: rule name, severity, matched text, action

This enables security teams to:
- Investigate suspicious agent behavior
- Audit session activity for compliance
- Review blocked requests with full context
- Analyze patterns across historical sessions

## Configuration

### Enable Session Storage

```yaml
storage:
  enabled: true
  path: "data/elida.db"
  retention_days: 30
  capture_mode: "all"  # "all" or "flagged_only"
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ELIDA_STORAGE_ENABLED` | `false` | Enable SQLite storage |
| `ELIDA_STORAGE_PATH` | `data/elida.db` | Database file path |
| `ELIDA_STORAGE_CAPTURE_MODE` | `all` | Capture mode |

### Capture Modes

| Mode | Description | Use Case |
|------|-------------|----------|
| `all` | Capture all session content (default) | Agentic workloads, fewer sessions, full audit trail |
| `flagged_only` | Only capture flagged sessions | High-volume web traffic, storage optimization |

**Recommendation**: Use `all` for AI agent traffic where sessions are fewer but higher-value. Use `flagged_only` for high-volume web traffic to reduce storage overhead.

## Session Record Schema

```sql
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    state TEXT NOT NULL,              -- Active, Killed, Completed, TimedOut
    start_time DATETIME NOT NULL,
    end_time DATETIME NOT NULL,
    duration_ms INTEGER NOT NULL,
    request_count INTEGER NOT NULL,
    bytes_in INTEGER NOT NULL,
    bytes_out INTEGER NOT NULL,
    backend TEXT NOT NULL,
    client_addr TEXT NOT NULL,
    metadata TEXT,                    -- JSON: custom key-value pairs
    captured_content TEXT,            -- JSON: array of requests/responses
    violations TEXT,                  -- JSON: array of policy violations
    created_at DATETIME
);
```

## Data Structures

### CapturedRequest

Each captured request/response pair:

```json
{
    "timestamp": "2026-01-18T22:44:18.059Z",
    "method": "POST",
    "path": "/v1/chat/completions",
    "request_body": "{\"messages\":[...]}",
    "response_body": "{\"choices\":[...]}",
    "status_code": 200
}
```

### Violation

Each policy violation:

```json
{
    "rule_name": "prompt_injection_ignore",
    "description": "LLM01: Prompt injection - instruction override",
    "severity": "critical",
    "matched_text": "ignore all previous instructions",
    "action": "block"
}
```

## API Endpoints

### List Session History

```bash
curl http://localhost:9090/control/history
```

Response:
```json
{
    "count": 42,
    "sessions": [
        {
            "id": "session-123",
            "state": "killed",
            "start_time": "2026-01-18T22:44:18Z",
            "end_time": "2026-01-18T22:45:00Z",
            "duration_ms": 42000,
            "request_count": 5,
            "bytes_in": 1024,
            "bytes_out": 8192,
            "backend": "anthropic",
            "client_addr": "10.0.0.5",
            "captured_content": [...],
            "violations": [...]
        }
    ]
}
```

### Get Session by ID

```bash
curl http://localhost:9090/control/history/{session_id}
```

### Query Parameters

| Parameter | Description | Example |
|-----------|-------------|---------|
| `limit` | Max results | `?limit=100` |
| `offset` | Pagination offset | `?offset=50` |
| `state` | Filter by state | `?state=killed` |
| `backend` | Filter by backend | `?backend=anthropic` |
| `since` | Start time filter | `?since=2026-01-18T00:00:00Z` |
| `until` | End time filter | `?until=2026-01-19T00:00:00Z` |

### Statistics

```bash
curl http://localhost:9090/control/history/stats
```

```bash
curl http://localhost:9090/control/history/timeseries?interval=hour&since=2026-01-18T00:00:00Z
```

## Storage Overhead

Estimated bytes per session:

| Component | Size | Notes |
|-----------|------|-------|
| Base session | ~250 bytes | ID, timestamps, metrics |
| Per captured request | ~2,500 bytes | With 2KB body capture |
| Per violation | ~300 bytes | Rule, description, match |

For a typical agentic session (10 requests, 2 violations):
- ~250 + (10 × 2,500) + (2 × 300) = **~25.85 KB**

### Retention

Configure `retention_days` to automatically clean up old records:

```yaml
storage:
  retention_days: 30  # Keep 30 days of history
```

The cleanup runs periodically when ELIDA is running.

## When Sessions Are Captured

Session records are saved when:

1. **Session killed** via API (`POST /control/sessions/{id}/kill`)
2. **Session terminated** via API (`POST /control/sessions/{id}/terminate`)
3. **Session timed out** (idle timeout or max duration)
4. **Policy auto-terminate** (rule with `action: terminate`)

Records are **not** saved for:
- Active sessions (still in progress)
- Sessions that haven't had any requests blocked

## Dashboard Integration

The web dashboard at `http://localhost:9090/` displays:

1. **History tab**: Browse historical sessions with filtering
2. **Session details modal**: Click any row to see:
   - Session metrics (duration, requests, bytes)
   - Captured requests with expandable bodies
   - Policy violations with matched patterns

## Best Practices

### For Agentic Workloads

```yaml
storage:
  enabled: true
  capture_mode: "all"  # Full audit trail
  retention_days: 90   # 90 days for compliance
```

### For High-Volume API Traffic

```yaml
storage:
  enabled: true
  capture_mode: "flagged_only"  # Only suspicious sessions
  retention_days: 30
```

### For Development/Testing

```yaml
storage:
  enabled: true
  capture_mode: "all"
  retention_days: 7   # Short retention
```

## Security Considerations

- Session records may contain sensitive data (API keys, PII)
- Secure the SQLite database file with appropriate permissions
- Consider encryption at rest for production deployments
- Implement access controls for the `/control/history` endpoint
- Be mindful of storage growth with `capture_mode: all`

## Comparison to Telecom CDRs

| Telecom CDR | ELIDA Session Record |
|-------------|---------------------|
| Caller/Callee | Client/Backend |
| Call duration | Session duration |
| Call status | Session state |
| Media type | Request method/path |
| Billing info | Bytes in/out |
| N/A | Policy violations |
| N/A | Request/response content |

ELIDA extends the CDR concept with:
- Full request/response capture (optional)
- Policy violation tracking
- Backend routing information
- Metadata for custom attributes
