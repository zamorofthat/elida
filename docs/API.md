# Control API

ELIDA exposes a control API on `:9090` for monitoring, session management, and the dashboard UI.

## Dashboard

Open `http://localhost:9090/` in your browser for the web dashboard.

## Endpoints

### Health & Stats

```bash
# Health check
curl http://localhost:9090/control/health

# Aggregate stats
curl http://localhost:9090/control/stats
```

### Sessions

```bash
# List active sessions
curl http://localhost:9090/control/sessions?active=true

# Get session details
curl http://localhost:9090/control/sessions/{session-id}

# View session history (persisted)
curl http://localhost:9090/control/history
```

### Session Lifecycle

```bash
# Kill a session — blocks all subsequent requests from that client
curl -X POST http://localhost:9090/control/sessions/{session-id}/kill

# Resume a killed session
curl -X POST http://localhost:9090/control/sessions/{session-id}/resume
```

### Policy & Flagging

```bash
# View flagged sessions (policy violations)
curl http://localhost:9090/control/flagged
```

### Voice Sessions (WebSocket)

```bash
# Live voice sessions
curl http://localhost:9090/control/voice

# Persisted voice CDRs with transcripts
curl http://localhost:9090/control/voice-history

# TTS request tracking
curl http://localhost:9090/control/tts
```

### Event Audit Log

```bash
# All events
curl http://localhost:9090/control/events

# Filter by session
curl http://localhost:9090/control/events?session_id=abc123

# Filter by type and severity
curl http://localhost:9090/control/events?type=violation_detected&severity=critical

# Events for specific session
curl http://localhost:9090/control/events/{session-id}

# Event statistics
curl http://localhost:9090/control/events/stats
```

**Event types:** `session_started`, `session_ended`, `violation_detected`, `policy_action`, `tool_called`, `tokens_used`, `risk_escalated`, `kill_requested`

## Kill Block Modes

When you kill a session, ELIDA blocks subsequent requests from that client. The block behavior depends on configuration:

| Mode | Behavior | Use Case |
|------|----------|----------|
| `duration` | Blocks for a specific time (e.g., 30m) | Standard rate limiting / cooldown |
| `until_hour_change` | Blocks until the clock hour changes | Aligns with session ID regeneration |
| `permanent` | Blocks until server restart | Compromised sessions |

Killed sessions return `403 Forbidden` until the block expires.

```bash
# Kill a runaway Claude Code session
curl -X POST http://localhost:9090/control/sessions/client-abc123/kill
# {"session_id":"client-abc123","status":"killed"}

# All subsequent requests from that IP → 403 until block expires
```
