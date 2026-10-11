# Control API

ELIDA exposes a control API on `:9090` for monitoring, session management, and the dashboard UI.

## Dashboard

Open `http://localhost:9090/` in your browser for the web dashboard.

## Endpoints

### Health & Stats

```bash
# Health check (unauthenticated). With semantic detection wired it also
# reports "decision": {"capability", "reason"}: the capability enum
# (inline, async_only, degraded, disabled) and a fixed reason. Model
# identity and counters are only on the authenticated /control/decision.
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

# Get one persisted session
curl http://localhost:9090/control/history/{session-id}
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

# Get one flagged session
curl http://localhost:9090/control/flagged/{session-id}
```

#### Semantic decision records

Three detail responses explain each semantic violation and enforcement action:

| Endpoint | Fields |
|----------|--------|
| `GET /control/sessions/{id}` | `semantic_decisions` |
| `GET /control/flagged/{id}` | `semantic_decisions`, `semantic_decisions_dropped` |
| `GET /control/history/{id}` | `semantic_decisions`, `semantic_decisions_dropped` |

`semantic_decisions` holds one record per semantic decision recorded as a violation (bounded at 256 per session, evidence-only records evicted first); never request content. Records are oldest first. Each one has `event_id`, `rule_name`, `event_category`, `severity`, `effective_severity`, `source_role`, `message_index`, `evidence_only`, `would_contribute_points`, `timestamp` and `evidence`. `evidence` is the decision metadata: `signal`, `probability`, `aux_probability`, `vetoed`, `model`, `model_version`, `model_checksum`, `threshold_set`, `decision_id`, `transform`, `transform_depth`, `window_start_byte`, `window_end_byte`, `coverage_complete`, `execution_mode` and `protection_scope`. `semantic_decisions_dropped` counts the records the bound evicted or dropped. The list endpoints (`GET /control/sessions`, `GET /control/flagged`, `GET /control/history`) leave both fields out to keep their payloads small.

A violation object (`violations[]` in the flagged and history responses) carries `semantic` for a semantic rule: the same metadata as `evidence`, from the rule's representative decision (the most consequential so far). Its `event_id` names that decision. That decision's record may have been evicted by the bound, so a lookup by `event_id` can miss.

When a session ID is reused, history (`GET /control/history/{id}`) keeps the earlier sessions' records ahead of the current session's, with duplicate `event_id`s removed. Carried records are capped so the total stays at 256 or fewer, keeping the newest. The current session's own records are always kept. `semantic_decisions_dropped` also counts the carried records the cap removed.

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

### Settings (Layered Configuration)

ELIDA supports dynamic settings management with a layered hierarchy. See [configuration.md](configuration.md#settings-hierarchy-layered-configuration) for details.

```bash
# Get merged settings (all layers combined)
curl http://localhost:9090/control/settings

# Save settings (applies instantly, no restart)
curl -X PUT http://localhost:9090/control/settings \
  -H "Content-Type: application/json" \
  -d '{"policy":{"mode":"audit"}}'

# Change semantic detection to enforce: needs explicit confirmation, and is
# refused with 400 while policy.mode is audit. Only decision.mode and
# decision.preprocessing.* change at runtime; a restart-only decision key set
# to anything but its value in force is refused with 400 naming it, and
# omitting one is never a change, so partial saves like these work even with
# a stale value in settings.yaml (see configuration.md, "Changing decision
# settings at runtime").
curl -X PUT 'http://localhost:9090/control/settings?confirm=enforce' \
  -H "Content-Type: application/json" \
  -d '{"decision":{"mode":"enforce"}}'

# Reset to defaults (removes UI overrides)
curl -X DELETE http://localhost:9090/control/settings
```

**Additional endpoints:**

```bash
# Get default settings (read-only, from elida.yaml + env vars)
curl http://localhost:9090/control/settings/defaults

# Get local overrides only (settings.yaml)
curl http://localhost:9090/control/settings/local

# Get diff — what's changed from defaults
curl http://localhost:9090/control/settings/diff
```

**Settings structure:**

```json
{
  "policy": {
    "enabled": true,
    "mode": "enforce",
    "preset": "standard",
    "risk_ladder": {
      "enabled": true,
      "warn_score": 5,
      "throttle_score": 15,
      "block_score": 30,
      "terminate_score": 50
    },
    "custom_rules": []
  },
  "capture": {
    "mode": "flagged_only",
    "max_capture_size": 10000,
    "max_per_session": 100
  },
  "failover": {
    "enabled": false,
    "max_retries": 2,
    "auto_select": true
  }
}
```
