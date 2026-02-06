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

### WebSocket Support & Voice Session Tracking

Added WebSocket proxy support for real-time AI agents (OpenAI Realtime API, Deepgram, ElevenLabs, LiveKit). Features SIP-inspired voice session control for per-conversation tracking and transcripts.

**Key Concepts:**

| Term | Description |
|------|-------------|
| **WebSocket Session** | The underlying WebSocket connection (1 per client) |
| **Voice Session** | A single conversation within a WebSocket (like a phone call) |
| **INVITE** | Start a new voice session (detected from protocol messages) |
| **BYE** | End a voice session |
| **Transcript** | Captured text from STT/TTS during the conversation |

**Voice Session States (SIP-inspired):**
```
Idle → Inviting → Active → Held → Terminated
                    ↑         ↓
                    └─────────┘ (Resume)
```

**Supported Protocols:**

| Protocol | INVITE Signal | BYE Signal | Transcript Source |
|----------|---------------|------------|-------------------|
| **OpenAI Realtime** | `session.create` | `error` or close | `response.audio_transcript.*`, `conversation.item.input_audio_transcription.*` |
| **Deepgram** | `Metadata` | close | `Results` with `channel.alternatives[].transcript` |
| **ElevenLabs** | `voice_settings` | `flush` | `text` field (TTS input) |
| **LiveKit** | `participant_joined` | `participant_left` | - |
| **Custom** | Regex pattern | Regex pattern | - |

**Voice Session API Endpoints:**

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/control/voice` | GET | List all voice sessions |
| `/control/voice/{wsSessionID}` | GET | List voice sessions for a WebSocket |
| `/control/voice/{wsSessionID}/{voiceID}` | GET | Get voice session with transcript |
| `/control/voice/{wsSessionID}/{voiceID}/bye` | POST | End voice session |
| `/control/voice/{wsSessionID}/{voiceID}/hold` | POST | Put on hold |
| `/control/voice/{wsSessionID}/{voiceID}/resume` | POST | Resume from hold |

**Example API Response with Transcript:**
```json
{
  "id": "a1b2c3d4",
  "parent_session_id": "ws-session-123",
  "state": "active",
  "turn_count": 3,
  "audio_duration_ms": 45000,
  "model": "gpt-4o-realtime",
  "voice": "alloy",
  "transcript": [
    {
      "timestamp": "2026-01-24T21:15:00Z",
      "speaker": "user",
      "text": "Hello, how are you?",
      "is_final": true,
      "source": "stt"
    },
    {
      "timestamp": "2026-01-24T21:15:02Z",
      "speaker": "assistant",
      "text": "I'm doing well! How can I help you today?",
      "is_final": true,
      "source": "stt"
    }
  ]
}
```

**Configuration:**
```yaml
websocket:
  enabled: true
  voice_sessions:
    enabled: true
    max_concurrent: 5
    cdr_per_session: true
    protocols:
      - openai_realtime
      - deepgram
      - elevenlabs
```

**Files added:**
- `internal/websocket/handler.go` — WebSocket proxy handler
- `internal/websocket/dial.go` — Backend connection handling
- `internal/websocket/frame.go` — Frame processing
- `internal/websocket/voice_session.go` — Voice session model and manager
- `internal/websocket/session_control.go` — Protocol parsers for INVITE/BYE detection
- `test/unit/websocket_test.go` — WebSocket tests
- `test/unit/voice_session_test.go` — Voice session tests

**Files modified:**
- `internal/config/config.go` — Added WebSocketConfig, VoiceSessionConfig
- `internal/session/session.go` — Added WebSocket tracking fields
- `internal/router/router.go` — Added WSURL derivation
- `internal/proxy/proxy.go` — Added WebSocket detection
- `internal/control/api.go` — Added voice session API endpoints
- `cmd/elida/main.go` — WebSocket handler initialization

### Voice CDR Persistence & TTS Tracking (January 2026)

Added persistent storage for voice session CDRs (Call Detail Records) and REST TTS request tracking. Voice transcripts are now saved to SQLite when sessions end, completing the telecom-inspired CDR model.

**What Gets Persisted:**

| Data Type | Source | Storage |
|-----------|--------|---------|
| Voice session metadata | WebSocket (OpenAI Realtime, Deepgram, etc.) | `voice_sessions` table |
| Full transcript | STT/TTS during conversation | JSON in `voice_sessions.transcript` |
| TTS requests | REST API (OpenAI TTS, Deepgram Aura) | `tts_requests` table |

**Voice CDR API Endpoints:**

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/control/voice` | GET | List **active** voice sessions (live) |
| `/control/voice-history` | GET | List **persisted** voice session CDRs |
| `/control/voice-history/stats` | GET | Aggregate voice session statistics |
| `/control/voice-history/{voiceID}` | GET | Get voice session with full transcript |
| `/control/tts` | GET | List TTS requests |
| `/control/tts/stats` | GET | Aggregate TTS statistics |

**Example Voice CDR (persisted):**
```json
{
  "id": "5b97e226",
  "parent_session_id": "client-abc123-mock",
  "state": "terminated",
  "model": "gpt-4o-realtime",
  "protocol": "openai_realtime",
  "duration_ms": 2500,
  "transcript": [
    {"timestamp": "2026-01-28T22:30:00Z", "speaker": "user", "text": "What is AI?", "source": "stt"},
    {"timestamp": "2026-01-28T22:30:02Z", "speaker": "assistant", "text": "AI is artificial intelligence...", "source": "stt"}
  ]
}
```

**Example TTS Request (tracked):**
```json
{
  "id": "f23a9e09",
  "session_id": "client-abc123-openai",
  "provider": "openai",
  "model": "tts-1",
  "voice": "nova",
  "text": "Hello, how can I help?",
  "text_length": 22,
  "response_bytes": 48000,
  "duration_ms": 150,
  "status_code": 200
}
```

**TTS Endpoint Detection:**

| Provider | Endpoint Pattern | Fields Extracted |
|----------|------------------|------------------|
| OpenAI | `/v1/audio/speech` | model, voice, input (text) |
| Deepgram Aura | `/v1/speak` | model, voice, text |
| ElevenLabs | `/text-to-speech/{voice_id}` | voice (from URL), text |

**Files added/modified:**
- `internal/storage/sqlite.go` — Added `voice_sessions` and `tts_requests` tables, `VoiceSessionRecord`, `TTSRequest` types
- `internal/proxy/proxy.go` — Added `isTTSRequest()` detection and TTS tracking
- `internal/control/api.go` — Added `/control/voice-history/*` and `/control/tts/*` endpoints
- `cmd/elida/main.go` — Wired voice session end callback for CDR persistence

**Testing:**
```bash
# Start with storage enabled
make run-storage

# Or with WebSocket + storage
make run-websocket

# Check voice CDRs after a voice session ends
curl http://localhost:9090/control/voice-history | jq .

# Check TTS requests
curl http://localhost:9090/control/tts | jq .

# Get stats
curl http://localhost:9090/control/voice-history/stats | jq .
curl http://localhost:9090/control/tts/stats | jq .
```

### Capture-All Mode (February 2026)

Added policy-independent capture-all mode for full audit/compliance requirements. When enabled, every request/response body is captured regardless of policy violations.

**Key Concepts:**

| Term | Description |
|------|-------------|
| **Capture Mode** | `flagged_only` (default) or `all` |
| **CaptureBuffer** | Policy-independent buffer for all request/response pairs |
| **Max Capture Size** | Per-body truncation limit (default 10KB) |
| **Max Per Session** | Maximum captured pairs per session (default 100) |

**How it works:**

```
Request arrives
      │
      ├─► Policy Engine (if enabled)
      │         │
      │         └─► Flagged? → Policy captures content
      │
      └─► CaptureBuffer (if capture_mode=all)
                │
                └─► Always captures request/response
                    (independent of policy)
```

**Priority at session end:**
1. If policy captured content → use policy captures (has violations)
2. If no policy captures but CaptureBuffer has content → use capture-all content
3. Both are saved to SQLite session history

**Configuration:**
```yaml
storage:
  enabled: true
  capture_mode: "all"             # Capture everything
  max_capture_size: 10000         # 10KB per body
  max_captured_per_session: 100   # Max pairs per session
```

**Environment variables:**
```bash
# Enable capture-all mode
ELIDA_STORAGE_ENABLED=true
ELIDA_STORAGE_CAPTURE_MODE=all
ELIDA_STORAGE_MAX_CAPTURE_SIZE=10000
ELIDA_STORAGE_MAX_CAPTURED_PER_SESSION=100
```

**Quick start:**
```bash
# Run with capture-all enabled
make run-demo    # or make run-storage

# Verify capture mode in health check
curl http://localhost:9090/control/health | jq .capture_mode
# Returns: "all"

# After some requests, check session history
curl http://localhost:9090/control/history | jq '.[0].captured_content'
```

**Example captured content:**
```json
{
  "id": "session-abc123",
  "request_count": 3,
  "captured_content": [
    {
      "timestamp": "2026-02-05T10:30:00Z",
      "method": "POST",
      "path": "/v1/chat/completions",
      "request_body": "{\"model\":\"gpt-4\",\"messages\":[...]}",
      "response_body": "{\"choices\":[{\"message\":{\"content\":\"...\"}}]}",
      "status_code": 200
    }
  ]
}
```

**Files added/modified:**
- `internal/proxy/capture.go` — NEW: CaptureBuffer implementation
- `internal/config/config.go` — Added MaxCaptureSize, MaxCapturedPerSession fields
- `internal/proxy/proxy.go` — Integrated CaptureBuffer into all streaming modes
- `cmd/elida/main.go` — Wired capture-all content into session end callback
- `internal/control/api.go` — Added capture_mode to health endpoint
- `test/unit/capture_test.go` — NEW: 13 tests for CaptureBuffer

### Voice/Speech Services Reference

ELIDA supports proxying to various voice AI services. Here's a reference for supported services, pricing, and how to test.

#### Service Comparison

| Service | Type | Free Tier | Paid Pricing | WebSocket API |
|---------|------|-----------|--------------|---------------|
| **Deepgram** | STT | 200 mins/month | ~$0.0043/min | ✅ Yes |
| **AssemblyAI** | STT | 100 hrs one-time | ~$0.01/min | ✅ Yes |
| **ElevenLabs** | TTS | 10K chars/month | ~$0.30/1K chars | ✅ Yes |
| **OpenAI Realtime** | STT+LLM+TTS | ❌ None | ~$0.06/min + tokens | ✅ Yes |
| **OpenAI Whisper** | STT (batch) | ❌ None | $0.006/min | ❌ REST only |
| **Google Cloud STT** | STT | 60 mins/month | $0.016/min | ✅ Yes |
| **Azure Speech** | STT/TTS | 5 hrs/month | $0.016/min | ✅ Yes |

**Legend:**
- **STT** = Speech-to-Text (transcribes audio to text)
- **TTS** = Text-to-Speech (generates audio from text)
- **STT+LLM+TTS** = Full conversational AI (speech in, speech out)

#### Deepgram (Recommended for Testing)

Best free tier for STT testing. 200 free minutes/month, no credit card required.

**Sign up:** https://deepgram.com

**Backend config:**
```yaml
backends:
  deepgram:
    url: "wss://api.deepgram.com/v1/listen"
    type: deepgram

websocket:
  enabled: true
  voice_sessions:
    enabled: true
    protocols: [deepgram]
```

**Test with wscat:**
```bash
# Through ELIDA
wscat -c "ws://localhost:8080/v1/listen?model=nova-2" \
  -H "Authorization: Token YOUR_DEEPGRAM_KEY"

# Send audio data (binary) and receive transcripts
```

**What ELIDA captures:**
- User speech transcripts (from `Results` messages)
- Interim and final transcripts
- Audio bytes in/out metrics

#### ElevenLabs

TTS service - converts text to natural-sounding speech. 10,000 free characters/month.

**Sign up:** https://elevenlabs.io

**Backend config:**
```yaml
backends:
  elevenlabs:
    url: "wss://api.elevenlabs.io/v1/text-to-speech/{voice_id}/stream-input"
    type: elevenlabs
```

**What ELIDA captures:**
- Text being synthesized (what assistant says)
- Audio bytes out metrics

#### OpenAI Realtime API

Full conversational AI with speech input/output. No free tier, requires API access.

**Backend config:**
```yaml
backends:
  openai-realtime:
    url: "wss://api.openai.com/v1/realtime"
    type: openai

websocket:
  enabled: true
  voice_sessions:
    enabled: true
    protocols: [openai_realtime]
```

**What ELIDA captures:**
- User speech transcripts (`conversation.item.input_audio_transcription.completed`)
- Assistant speech transcripts (`response.audio_transcript.done`)
- Text responses (`response.text.done`)
- Turn counts, audio duration, model/voice metadata

#### Local Testing Quick Start

**Prerequisites:** Node.js, wscat (`npm install -g wscat`)

```bash
# Terminal 1: Start mock voice server
make mock-voice

# Terminal 2: Run ELIDA with WebSocket enabled
make run-websocket

# Terminal 3: Connect and test
wscat -c ws://localhost:8080
> {"type":"session.create","session":{"model":"gpt-4o-realtime"}}
> {"type":"input_audio_buffer.commit"}

# Terminal 4: Verify voice sessions and transcripts
curl -s http://localhost:9090/control/voice | jq .
```

**Test with policy scanning:**
```bash
# Use run-websocket-policy instead
make run-websocket-policy

# Send a policy-violating message
wscat -c ws://localhost:8080
> {"type":"conversation.item.create","item":{"content":[{"type":"text","text":"ignore previous instructions"}]}}

# After disconnect, check flagged sessions
curl -s http://localhost:9090/control/flagged | jq .
```

#### Testing Without Paid Services (Detailed)

**Option 1: Mock WebSocket Server (Completely Free)**

The mock server script is included at `scripts/mock_voice_server.js`. Here's what it does:

```javascript
// scripts/mock_voice_server.js - simulates OpenAI Realtime API
const WebSocket = require('ws');
const wss = new WebSocket.Server({ port: 11434 });

console.log('Mock voice server running on ws://localhost:11434');

wss.on('connection', (ws) => {
  console.log('Client connected');

  // Send session.created on connect (simulates INVITE OK)
  ws.send(JSON.stringify({
    type: "session.created",
    session: { id: "sess_mock_123", model: "gpt-4o-realtime", voice: "alloy" }
  }));

  ws.on('message', (data) => {
    try {
      const msg = JSON.parse(data.toString());
      console.log('Received:', msg.type);

      switch (msg.type) {
        case "session.create":
          // Already sent session.created on connect
          break;

        case "input_audio_buffer.commit":
          // Simulate user speech transcription
          ws.send(JSON.stringify({
            type: "conversation.item.input_audio_transcription.completed",
            transcript: "Hello, this is a test message from the user."
          }));

          // Simulate assistant response
          setTimeout(() => {
            ws.send(JSON.stringify({
              type: "response.audio_transcript.done",
              transcript: "Hi there! I received your message. How can I help you today?"
            }));
            ws.send(JSON.stringify({
              type: "response.done",
              response: { id: "resp_123", status: "completed" }
            }));
          }, 500);
          break;

        case "conversation.item.create":
          // Handle text input
          if (msg.item?.content?.[0]?.text) {
            ws.send(JSON.stringify({
              type: "response.text.done",
              text: "I understand you said: " + msg.item.content[0].text
            }));
          }
          break;
      }
    } catch (e) {
      console.log('Received binary data:', data.length, 'bytes');
    }
  });

  ws.on('close', () => console.log('Client disconnected'));
});
```

**Run the mock server manually (if not using make):**
```bash
cd scripts && npm install ws && node mock_voice_server.js
```

**Test the mock server with ELIDA (manual config):**
```bash
# Terminal 1: Run mock server
make mock-voice

# Terminal 2: Run ELIDA (uses configs/elida.yaml by default)
make run-websocket

# Or with custom config:
cat > /tmp/elida-test.yaml << 'EOF'
listen: ":8080"
backends:
  mock:
    url: "ws://localhost:11434"
    default: true
websocket:
  enabled: true
  voice_sessions:
    enabled: true
    protocols: [openai_realtime]
control:
  enabled: true
  listen: ":9090"
EOF

./bin/elida -config /tmp/elida-test.yaml

# Terminal 3: Connect through ELIDA
wscat -c ws://localhost:8080

# Send messages:
{"type":"session.create","session":{"model":"gpt-4o-realtime"}}
{"type":"input_audio_buffer.commit"}

# Terminal 4: Check voice sessions with transcripts
curl -s http://localhost:9090/control/voice | jq .
```

**Option 2: Local Whisper (Free, Self-Hosted STT)**

Run speech-to-text locally using Whisper:

```bash
# Using whisper.cpp (C++, fast)
git clone https://github.com/ggerganov/whisper.cpp
cd whisper.cpp
make
./models/download-ggml-model.sh base
./main -m models/ggml-base.bin -f samples/jfk.wav

# Using faster-whisper (Python)
pip install faster-whisper
python -c "
from faster_whisper import WhisperModel
model = WhisperModel('base')
segments, info = model.transcribe('audio.wav')
for segment in segments:
    print(segment.text)
"
```

Note: Local Whisper is batch-mode only (not real-time WebSocket), but useful for testing transcription accuracy.

**Option 3: Browser MediaRecorder + WebSocket**

For testing real audio input through ELIDA:

```html
<!-- Save as test_voice.html, open in browser -->
<!DOCTYPE html>
<html>
<head><title>ELIDA Voice Test</title></head>
<body>
  <button id="start">Start Recording</button>
  <button id="stop" disabled>Stop</button>
  <pre id="transcript"></pre>
  <script>
    const ws = new WebSocket('ws://localhost:8080/v1/listen?model=nova-2');
    let mediaRecorder;

    ws.onopen = () => console.log('Connected to ELIDA');
    ws.onmessage = (e) => {
      const msg = JSON.parse(e.data);
      if (msg.channel?.alternatives?.[0]?.transcript) {
        document.getElementById('transcript').textContent +=
          msg.channel.alternatives[0].transcript + '\n';
      }
    };

    document.getElementById('start').onclick = async () => {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      mediaRecorder = new MediaRecorder(stream, { mimeType: 'audio/webm' });
      mediaRecorder.ondataavailable = (e) => ws.send(e.data);
      mediaRecorder.start(250); // Send chunks every 250ms
      document.getElementById('start').disabled = true;
      document.getElementById('stop').disabled = false;
    };

    document.getElementById('stop').onclick = () => {
      mediaRecorder.stop();
      document.getElementById('start').disabled = false;
      document.getElementById('stop').disabled = true;
    };
  </script>
</body>
</html>
```

### WebSocket Policy Integration

ELIDA applies policy scanning to WebSocket connections using a **telecom-inspired model**: real-time scanning for control messages, post-session analysis for voice transcripts.

#### Two-Layer Scanning

| Layer | When | What | Actions |
|-------|------|------|---------|
| **Real-time** | During connection | Text frames (JSON messages) | Block frame, terminate connection |
| **Post-session** | Voice session ends | Full transcript | Flag for review, capture content |

**Why this approach?**
- Telecom/VoIP systems don't block calls based on content in real-time
- Audio requires STT before text analysis (adds latency)
- Transcripts are already captured from the voice service
- Post-session scanning matches the CDR (Call Detail Record) pattern

#### Real-Time Text Frame Scanning

Text frames (JSON protocol messages) are scanned as they pass through:

```
Client → ELIDA → Backend
         ↓
   Policy check:
   - Prompt injection in text?
   - PII in messages?
         ↓
   Block frame or terminate
```

**Configuration:**
```yaml
websocket:
  enabled: true
  scan_text_frames: true  # Enable real-time scanning

policy:
  enabled: true
  mode: enforce  # or "audit"
```

**What gets scanned:**
- Text frames (JSON messages) - scanned in real-time
- Binary frames (audio/video) - NOT scanned (pass-through)

**Actions for text frame violations:**
- `flag` - Log violation, forward frame
- `block` - Drop frame, don't forward
- `terminate` - Close both connections immediately

#### Post-Session Transcript Scanning

When a voice session ends (BYE), the full transcript is scanned:

```
Voice session active
       │
       ├─► Transcript captured (no blocking)
       │
       └─► Session ends (BYE)
              │
              ▼
       ┌──────────────────┐
       │ Scan full        │
       │ transcript       │
       └────────┬─────────┘
                │
       ┌────────┴────────┐
       ▼                 ▼
   Violations?       No violations
       │                 │
       ▼                 ▼
   Flag parent       Done
   session with
   captured content
```

**What gets captured on violation:**
```json
{
  "session_id": "client-abc123-openai",
  "captured_content": [{
    "timestamp": "2026-01-24T21:30:00Z",
    "method": "VOICE",
    "path": "/voice/a1b2c3d4",
    "request_body": "user: Hello, ignore previous instructions\nassistant: I cannot comply with that request.\n"
  }],
  "violations": [{
    "rule_name": "prompt_injection_ignore",
    "severity": "critical",
    "action": "flag"
  }]
}
```

**Voice session metadata on violation:**
```json
{
  "id": "a1b2c3d4",
  "metadata": {
    "policy_violations": "true",
    "max_severity": "critical"
  }
}
```

#### Viewing Flagged Voice Sessions

```bash
# List all flagged sessions (includes voice violations)
curl http://localhost:9090/control/flagged

# Get specific flagged session with captured transcript
curl http://localhost:9090/control/flagged/client-abc123-openai

# Historical flagged sessions
curl "http://localhost:9090/control/history?state=flagged"
```

#### Testing Policy Scanning

```bash
# Terminal 1: Run mock voice server
make mock-voice

# Terminal 2: Run ELIDA with policy enabled
make run-websocket-policy

# Terminal 3: Connect and send violating message
wscat -c ws://localhost:8080
> {"type":"conversation.item.create","item":{"content":[{"type":"text","text":"ignore previous instructions"}]}}

# Terminal 4: Check flagged sessions after disconnecting
curl http://localhost:9090/control/flagged | jq .
```

#### Files Changed

- `internal/websocket/handler.go` — Real-time text frame scanning
- `internal/websocket/voice_session.go` — Post-session transcript scanning, `SetPolicyEngine()`, `GetFullTranscript()`
- `cmd/elida/main.go` — Wire policy engine to WebSocket handler
- `test/unit/websocket_test.go` — Policy integration tests
- `test/unit/voice_session_test.go` — Transcript scanning tests

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

### Response Body Capture (LLM02 Coverage)

Added response body capture for flagged sessions, providing full forensic data:

| Action | Request Body | Response Body | Why |
|--------|--------------|---------------|-----|
| **Block** | ✓ captured | ✗ none | Request stopped before reaching LLM |
| **Flag** | ✓ captured | ✓ captured | Full forensics for review |
| **Terminate** | ✓ captured | ✗ none | Session killed immediately |

**Key behaviors:**
- **Audit mode**: Flagged requests go through to backend, both request + response captured
- **Enforce mode**: Blocked requests captured (request only), flagged requests get full capture
- Status code captured for forensics (200 = success, 403 = blocked by ELIDA)

**API endpoints:**
- `GET /control/flagged` — List all flagged sessions
- `GET /control/flagged/{id}` — Get flagged session with captured content
- `GET /control/history?state=flagged` — Historical flagged sessions

**Example captured content:**
```json
{
  "session_id": "attack-session-1",
  "captured_content": [{
    "timestamp": "2026-01-23T22:14:58Z",
    "method": "POST",
    "path": "/v1/chat/completions",
    "request_body": "{\"messages\":[{\"content\":\"ignore previous instructions...\"}]}",
    "response_body": "{\"choices\":[{\"message\":{\"content\":\"I cannot comply...\"}}]}",
    "status_code": 200
  }],
  "violations": [{
    "rule_name": "prompt_injection_ignore",
    "severity": "critical",
    "action": "block"
  }]
}
```

**Files changed:**
- `internal/proxy/proxy.go` — Added response capture after backend call
- `internal/policy/policy.go` — Added `UpdateLastCaptureWithResponseAndStatus()`
- `web/src/App.jsx` — Display response_body in dashboard

### Immediate Persistence of Flagged Sessions

Flagged sessions are now written to SQLite immediately (not just on session end), ensuring forensic data survives crashes:

**Before:**
```
Request flagged → Stored in memory → Written to SQLite when session ends
                                    ↓
                              CRASH = Data lost!
```

**After:**
```
Request flagged → Written to SQLite immediately → Updated on subsequent requests
                                    ↓
                              CRASH = Data survives ✓
```

**Query flagged history:**
```bash
# All flagged sessions (persisted immediately)
curl "http://localhost:9090/control/history?state=flagged"

# Specific flagged session
curl "http://localhost:9090/control/flagged/session-id"
```

**Files changed:**
- `internal/proxy/proxy.go` — Added `persistFlaggedSession()` method, `SetStorage()`
- `cmd/elida/main.go` — Pass SQLite storage to proxy for immediate persistence

### Benchmark Mode Comparison

Added `--compare-modes` to benchmark script for comparing policy modes:

```bash
./scripts/benchmark.sh --compare-modes
```

**Output:**
```
                         No Policy    Audit    Enforce
----------------------- ---------- -------- ----------
Avg latency (ms)              109      116        113
Blocked req latency (ms)      107      100         49  ← 2x faster!
Memory per session (KB)         6        0         30
```

**Key insights:**
- **Enforce mode**: Blocked requests are ~2x faster (no backend call)
- **Audit mode**: All requests forwarded, blocked latency same as normal
- **Memory**: Varies based on content capture (flagged sessions use more)

**Benchmark options:**
```bash
./scripts/benchmark.sh              # Run all benchmarks
./scripts/benchmark.sh --memory     # Memory profiling only
./scripts/benchmark.sh --latency    # Latency test only
./scripts/benchmark.sh --sessions   # Session creation throughput
./scripts/benchmark.sh --policy     # Policy evaluation overhead
./scripts/benchmark.sh --compare-modes  # Compare all policy modes
./scripts/benchmark.sh --help       # Show all options
```

**Files changed:**
- `scripts/benchmark.sh` — Added `--compare-modes`, macOS millisecond fix, improved memory measurement

### Session Records: Capture Modes

ELIDA follows the telecom CDR (Call Detail Record) model with two capture modes:

| Capture Mode | Description | Use Case |
|--------------|-------------|----------|
| `flagged_only` | Only policy-flagged sessions get request/response bodies | Production (default) |
| `all` | Every request/response captured for all sessions | Audit/compliance mode |

**What gets captured by mode:**

| Data | flagged_only | all |
|------|--------------|-----|
| Session ID | ✓ | ✓ |
| Duration | ✓ | ✓ |
| Request count | ✓ | ✓ |
| Bytes in/out | ✓ | ✓ |
| Backend used | ✓ | ✓ |
| Client IP | ✓ | ✓ |
| Start/end time | ✓ | ✓ |
| **Request body** | Flagged only | ✓ All |
| **Response body** | Flagged only | ✓ All |
| **Violations** | ✓ | ✓ |

**Configuration:**
```yaml
storage:
  enabled: true
  capture_mode: "flagged_only"    # or "all" for full audit
  max_capture_size: 10000         # 10KB per body (truncates with ...[truncated])
  max_captured_per_session: 100   # Max request/response pairs per session
```

**Environment variables:**
```bash
ELIDA_STORAGE_CAPTURE_MODE=all
ELIDA_STORAGE_MAX_CAPTURE_SIZE=10000
ELIDA_STORAGE_MAX_CAPTURED_PER_SESSION=100
```

**Rationale:** `flagged_only` is the default because capturing every request/response body can be expensive for high-traffic deployments. Use `all` mode for audit/compliance requirements where full content capture is needed.

---

## Tech Stack

- **Language:** Go 1.24+
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
│   ├── proxy/
│   │   ├── proxy.go            # Core proxy logic
│   │   └── capture.go          # CaptureBuffer for policy-independent capture
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
- [x] WebSocket proxy for voice/real-time agents (OpenAI Realtime, Deepgram, ElevenLabs, LiveKit)
- [x] Voice session tracking with SIP-inspired lifecycle (INVITE/BYE/Hold/Resume)
- [x] Transcript capture and post-session policy scanning
- [x] Response body scanning (LLM02 - Insecure Output Handling)

### Not Yet Implemented
- [ ] Real-time speech analytics (live sentiment/coaching during voice sessions)
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

### Real-Time Speech Analytics

**Status:** Planned
**Use Case:** Provide advisory insights during live voice conversations without blocking.

This follows the enterprise contact center pattern where real-time analysis provides guidance to agents/operators without interrupting the conversation.

**Architecture:**
```
Voice session active
       │
       ├─► Transcript event arrives (from STT)
       │         │
       │         ▼
       │   ┌─────────────┐
       │   │ Analyze     │
       │   │ content     │
       │   └──────┬──────┘
       │          │
       │          ▼
       │   ┌─────────────┐
       │   │ Emit alert  │──► Dashboard / Webhook / SSE
       │   │ (advisory)  │
       │   └─────────────┘
       │
       └─► Conversation continues (NOT blocked)
```

**Key principle:** Advisory only. Unlike post-session scanning which flags for review, real-time analytics provides live insights without interrupting the conversation.

**Potential insights:**

| Category | Examples |
|----------|----------|
| **Sentiment** | "Customer frustrated", "Positive tone" |
| **Topics** | "Discussing refund", "Billing inquiry" |
| **Compliance** | "Disclosure not read", "Missing verification" |
| **Risk** | "PII mentioned", "Prompt injection attempt" |
| **Coaching** | "Mention retention offer", "Escalate to supervisor" |

**Implementation options:**

| Approach | Latency | Use Case |
|----------|---------|----------|
| **Pattern matching** | ~0ms | Simple keyword detection |
| **Local classifier** | 50-200ms | Sentiment, topic classification |
| **LLM analysis** | 200-500ms | Complex reasoning, coaching suggestions |

**Alert delivery mechanisms:**

1. **WebSocket to dashboard** — Real-time UI updates
2. **Webhook** — Integration with external systems (Slack, PagerDuty)
3. **SSE endpoint** — Lightweight monitoring stream
4. **Redis pub/sub** — Multi-instance alert distribution

**Proposed config:**
```yaml
websocket:
  voice_sessions:
    enabled: true
    realtime_analytics:
      enabled: true
      mode: advisory           # advisory (log only) or alert (emit events)
      analyzers:
        - type: sentiment
          threshold: negative  # alert on negative sentiment
        - type: pattern
          patterns: ["refund", "cancel", "lawsuit"]
          alert_name: "escalation_keywords"
      alerts:
        webhook: "https://hooks.slack.com/..."
        sse_endpoint: true     # Enable /control/voice/alerts SSE stream
```

**Proposed API:**

```bash
# SSE stream of real-time alerts
curl -N http://localhost:9090/control/voice/alerts

# Example event:
data: {"session_id":"abc123","voice_id":"v1","type":"sentiment","value":"negative","transcript":"I want to cancel everything","timestamp":"2026-01-24T21:30:00Z"}

# Get alerts for specific session
curl http://localhost:9090/control/voice/{wsSessionID}/{voiceID}/alerts
```

**Differs from current implementation:**

| Current (Implemented) | Future (Real-Time Analytics) |
|-----------------------|------------------------------|
| Post-session scanning | During-session analysis |
| Policy violations only | Sentiment, topics, coaching |
| Flag for review | Live advisory alerts |
| Captured in session record | Streamed to dashboard/webhook |

**Implementation phases:**

1. **Phase 1:** Pattern-based alerts (keyword detection)
2. **Phase 2:** Sentiment analysis (local classifier)
3. **Phase 3:** LLM-powered coaching (optional external call)

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

### On-Prem Enterprise Roadmap

**Deployment Model:** Self-hosted, single-organization (not SaaS)

This simplifies the roadmap — no multi-tenancy, billing, or customer isolation needed.

#### Priority 1: Control API Authentication ✅

**Status:** Implemented
**Why:** Enterprises won't deploy without auth on the control plane.

Simple API key authentication for all `/control/*` endpoints.

```yaml
control:
  enabled: true
  listen: ":9090"
  auth:
    enabled: true
    api_key: "${ELIDA_CONTROL_API_KEY}"  # Required when auth enabled
```

```bash
# Without auth: 401 Unauthorized
curl http://localhost:9090/control/sessions

# With auth: 200 OK
curl -H "Authorization: Bearer ${ELIDA_CONTROL_API_KEY}" \
  http://localhost:9090/control/sessions
```

#### Priority 2: Per-Model Rate Limits

**Status:** Planned
**Why:** Control costs across different LLM backends.

```yaml
rate_limits:
  global:
    requests_per_minute: 1000
  per_model:
    "gpt-4*":
      requests_per_minute: 100
      tokens_per_day: 1000000
    "claude-3-opus*":
      requests_per_minute: 50
```

#### Priority 3: Dashboard Enhancements

**Status:** Planned
**Why:** Nice-to-have for SOC visibility.

- Real-time session graphs
- Policy violation trends
- Cost tracking by model/backend
- Export to CSV/JSON

#### Not Planned: Multi-Tenancy

For SaaS deployments, you'd need:
- Tenant isolation (separate sessions, policies, Redis keys)
- Per-tenant API keys
- Usage metering/billing
- Tenant onboarding API

**Decision:** Focus on on-prem single-org. Multi-tenancy adds complexity without value for self-hosted deployments.

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

### General Conventions
- Standard Go conventions
- `slog` for structured logging
- Interfaces for testability (see `session.Store`)
- Context for cancellation
- Graceful shutdown handling

### Linter Requirements (CI enforced)

**IMPORTANT:** Always run `make fmt` before committing. CI runs `golangci-lint` and will fail on formatting issues.

**gofmt rules:**
- Use tabs for indentation, not spaces
- Struct field alignment: Do NOT manually align struct fields with extra spaces
  ```go
  // BAD - manual alignment causes gofmt failures
  type Config struct {
      Enabled              bool   `yaml:"enabled"`
      MaxSize              int    `yaml:"max_size"`
  }

  // GOOD - let gofmt handle it naturally
  type Config struct {
      Enabled bool `yaml:"enabled"`
      MaxSize int  `yaml:"max_size"`
  }
  ```
- Same applies to variable declarations and comments

**Common patterns:**
```go
// Error handling - always check errors
if err != nil {
    return fmt.Errorf("operation failed: %w", err)
}

// Prefer early returns over deep nesting
if !valid {
    return errors.New("invalid input")
}
// continue with main logic...

// Use named return values sparingly, mainly for documentation
func (s *Store) Get(id string) (session *Session, found bool)
```

**Before committing:**
```bash
make fmt      # Format all Go files
make lint     # Run full linter suite (optional, CI will catch issues)
go build ./...  # Verify compilation
make test     # Run tests
```

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

# WebSocket / Voice Sessions
make mock-voice            # Start mock voice server (simulates OpenAI Realtime)
make run-websocket         # Run with WebSocket proxy enabled
make run-websocket-policy  # Run with WebSocket + policy scanning
make run-websocket-mock    # Run ELIDA + mock server together

# Code quality
make fmt                # Format code
make lint               # Run golangci-lint (requires golangci-lint)

# Quick verification
make test-ollama        # Test basic proxy against Ollama
make test-stream        # Test streaming
make sessions           # View active sessions
make stats              # View stats

# Benchmarking
./scripts/benchmark.sh              # Run all benchmarks
./scripts/benchmark.sh --memory     # Memory profiling only
./scripts/benchmark.sh --latency    # Latency test only
./scripts/benchmark.sh --sessions   # Session creation throughput
./scripts/benchmark.sh --policy     # Policy evaluation overhead
./scripts/benchmark.sh --compare-modes  # Compare no-policy vs audit vs enforce

# Manual testing
curl http://localhost:8080/api/tags              # Test against Ollama
curl http://localhost:9090/control/sessions      # Check sessions
curl -X POST http://localhost:9090/control/sessions/{id}/kill  # Kill a session

# Flagged sessions
curl http://localhost:9090/control/flagged                    # List flagged sessions

# Voice sessions (WebSocket - live)
curl http://localhost:9090/control/voice                      # List all voice sessions
curl http://localhost:9090/control/voice/{wsSessionID}        # List voice sessions for WebSocket
curl http://localhost:9090/control/voice/{wsSessionID}/{voiceID}  # Get voice session with transcript
curl -X POST http://localhost:9090/control/voice/{wsSessionID}/{voiceID}/bye  # End voice session
curl -X POST http://localhost:9090/control/voice/{wsSessionID}/{voiceID}/hold # Put on hold

# Voice session history (persisted CDRs)
curl http://localhost:9090/control/voice-history              # List voice session CDRs
curl http://localhost:9090/control/voice-history/stats        # Voice session statistics
curl http://localhost:9090/control/voice-history/{id}         # Get voice session with transcript

# TTS requests (REST-based text-to-speech)
curl http://localhost:9090/control/tts                        # List TTS requests
curl http://localhost:9090/control/tts/stats                  # TTS statistics

curl http://localhost:9090/control/flagged/{id}               # Get flagged session details
curl "http://localhost:9090/control/history?state=flagged"    # Historical flagged sessions
```

## Test Coverage

Tests are in `test/` directory (black-box testing using only exported APIs):

```bash
make test              # Unit tests only (87 tests, fast)
make test-integration  # Integration tests (10 tests, requires Redis)
make test-all          # All tests (97 tests)
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
| `test/unit/` | `capture_test.go` | CaptureBuffer: Capture, UpdateLastResponse, Truncation, MaxPerSession, PeekContent, Remove, MultipleSessions, ConcurrentAccess, Defaults |
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
- `ELIDA_STORAGE_CAPTURE_MODE` — Capture mode: `flagged_only` (default) or `all` for full audit
- `ELIDA_STORAGE_MAX_CAPTURE_SIZE` — Max bytes per request/response body (default: `10000`)
- `ELIDA_STORAGE_MAX_CAPTURED_PER_SESSION` — Max request/response pairs per session (default: `100`)
- `ELIDA_POLICY_ENABLED` — Enable policy engine (default: `false`)
- `ELIDA_POLICY_MODE` — Policy mode: `enforce` (default) or `audit` (dry-run)
- `ELIDA_POLICY_CAPTURE` — Capture request content for flagged sessions (default: `true`)
- `ELIDA_POLICY_PRESET` — Policy preset: `minimal`, `standard`, or `strict`
- `ELIDA_CONTROL_AUTH_ENABLED` — Enable control API authentication (default: `false`)
- `ELIDA_CONTROL_API_KEY` — API key for control API (auto-enables auth when set)

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
