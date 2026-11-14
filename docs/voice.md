# Voice & WebSocket Sessions

ELIDA supports WebSocket proxying for real-time voice AI agents, with a SIP-inspired session lifecycle.

## Configuration

```yaml
websocket:
  enabled: true
  voice_sessions:
    enabled: true
    max_concurrent: 5
    protocols:
      - openai_realtime    # OpenAI Realtime API
      - deepgram           # Deepgram STT
      - elevenlabs         # ElevenLabs TTS
      # - livekit          # LiveKit
```

`protocols` restricts which parsers may claim a frame. Leaving it out enables
every built-in protocol; naming an unrecognised one enables nothing, so a typo
fails closed rather than quietly widening what a frame may be parsed as.

Both switches are off by default, and both are required: `websocket.enabled`
brings up the transport, and `voice_sessions.enabled` records sessions on it.
`GET /control/stats` reports the combination as `capabilities.voice`, and the
dashboard hides the Voice tab when it is false — a tab that can only say "No
voice sessions" reads as "none happened" rather than "this is switched off".

## Session Lifecycle

Voice sessions follow a SIP-inspired state machine:

```
INVITE → Active → Hold/Resume → BYE
```

- **INVITE** — Session starts (detected from protocol-specific messages)
- **Active** — Conversation in progress, transcripts captured
- **Hold/Resume** — Pause and continue
- **BYE** — Session ends, CDR persisted with full transcript

## Running

```bash
# Run with WebSocket enabled
make run-websocket

# Run with WebSocket + policy scanning
make run-websocket-policy
```

## Testing with mock server

No API keys needed:

```bash
# Terminal 1: Start mock voice server
make mock-voice

# Terminal 2: Start ELIDA with WebSocket
make run-websocket

# Terminal 3: Connect
wscat -c ws://localhost:8080
```

## Monitoring

```bash
# Live voice sessions
curl http://localhost:9090/control/voice

# Persisted CDRs with full transcripts
curl http://localhost:9090/control/voice-history

# TTS request tracking
curl http://localhost:9090/control/tts
```

## CDR (Call Detail Records)

When a voice session ends, ELIDA persists a CDR containing:

- Session start/end timestamps
- Protocol used
- Full transcript of the conversation
- Session metadata

This mirrors how telecom SBCs generate CDRs for billing and compliance.
