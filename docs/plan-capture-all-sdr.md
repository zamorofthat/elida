# Capture-All Session Detail Records (SDR)

## Goal
Wire up the existing (but unused) `CaptureMode` config so that when set to `"all"`, every request/response body is captured and persisted — not just policy-flagged ones. This enables full audit/compliance mode.

## Current State
- `CaptureMode` config exists in `config.go` with `"all"` / `"flagged_only"` values
- `ELIDA_STORAGE_CAPTURE_MODE` env var is wired
- **Neither is ever checked** — system always behaves as `flagged_only`
- Capture only happens inside the policy engine's `CaptureRequest()` when violations occur
- SQLite schema already supports `captured_content` JSON column

## Design: CaptureBuffer (proxy-level, policy-independent)

Create a `CaptureBuffer` in the proxy package that stores request/response bodies per session, independent of the policy engine. This keeps policy and capture concerns separate.

```
Request → Read body → Policy scan (if enabled) → CaptureBuffer.Capture() → Forward
Response ← Read body ← Policy scan (if enabled) ← CaptureBuffer.UpdateLastResponse() ← Backend
Session ends → CaptureBuffer.GetContent() → SQLite persistence
```

## Files to Modify

| File | Change |
|------|--------|
| `internal/config/config.go` | Change default to `"flagged_only"`, add `MaxCaptureSize`, `MaxCapturedPerSession` fields |
| `internal/proxy/capture.go` | **New file** — `CaptureBuffer` struct with `Capture()`, `UpdateLastResponse()`, `GetContent()` |
| `internal/proxy/proxy.go` | Add `captureBuffer` + `captureAll` fields, capture in `ServeHTTP` + all streaming handlers |
| `internal/session/manager.go` | Add `GetSessionEndCallback()` getter |
| `cmd/elida/main.go` | Wire capture buffer into session end callback, merge with policy content |
| `internal/control/api.go` | Add `capture_mode` to health endpoint response |
| `configs/elida.yaml` | Document new options |
| `web/src/App.jsx` | No changes needed (existing modal already renders `captured_content`) |

## Implementation Steps

### 1. Config (`internal/config/config.go`)
- Change default `CaptureMode` from `"all"` to `"flagged_only"`
- Add `MaxCaptureSize int` (default 10KB) and `MaxCapturedPerSession int` (default 100) to `StorageConfig`
- Add env var overrides
- Add validation

### 2. CaptureBuffer (`internal/proxy/capture.go` — new file)
- `CaptureBuffer` struct with thread-safe map of session ID → `[]CapturedRequest`
- `Capture(sessionID, CapturedRequest)` — stores request body, truncates if > max size
- `UpdateLastResponse(sessionID, body, statusCode)` — attaches response to last capture
- `GetContent(sessionID)` — returns and removes content (for session end)
- `PeekContent(sessionID)` — returns without removing (for periodic flush)
- `HasContent(sessionID)` / `Remove(sessionID)` — utilities

### 3. Wire into Proxy (`internal/proxy/proxy.go`)
- Add `captureBuffer *CaptureBuffer` and `captureAll bool` to Proxy struct
- Initialize in `NewWithRouter()` when `cfg.Storage.CaptureMode == "all"` and storage enabled
- In `ServeHTTP()`: after reading request body, call `captureBuffer.Capture()`
- In `handleStandard()`: after reading response, call `captureBuffer.UpdateLastResponse()`
- In `handleStreamingDirect/Chunked/WithBuffer`: join chunks, call `UpdateLastResponse()`
- Add `GetCaptureBuffer()` and `IsCaptureAll()` getters
- Add periodic flush every 10th request for long-running sessions

### 4. Session End Callback (`cmd/elida/main.go`)
- Declare `var proxyCaptureBuf *proxy.CaptureBuffer` before callback closure
- In session end callback: if capture buffer has content and policy didn't already capture, merge capture-all content into the `SessionRecord`
- After proxy creation: set `proxyCaptureBuf = proxyHandler.GetCaptureBuffer()`

### 5. Manager Getter (`internal/session/manager.go`)
- Add `GetSessionEndCallback() SessionEndCallback` method

### 6. Control API (`internal/control/api.go`)
- Add `capture_mode` field to health endpoint response
- Add `captureMode string` field to `Handler` struct

### 7. Config File (`configs/elida.yaml`)
- Document `capture_mode`, `max_capture_size`, `max_captured_per_session`

## Safeguards
- **Max body size**: 10KB default, truncated with `...[truncated]` suffix
- **Max entries per session**: 100 request/response pairs, then drops new captures
- **Periodic flush**: Every 10th request to survive crashes
- **No policy dependency**: Works even if policy engine is disabled
- **Memory bounded**: Limited by max entries × max body size per session

## Configuration

```yaml
storage:
  enabled: true
  capture_mode: "all"           # "all" or "flagged_only" (default)
  max_capture_size: 10000       # 10KB per body
  max_captured_per_session: 100 # Max pairs per session
```

```bash
ELIDA_STORAGE_CAPTURE_MODE=all
ELIDA_STORAGE_MAX_CAPTURE_SIZE=10000
```

## Verification
1. `make test` — existing tests pass
2. `make run-storage` with `ELIDA_STORAGE_CAPTURE_MODE=all` — start server
3. Run demo script — send normal + attack requests
4. `curl http://localhost:9090/control/history | jq .` — ALL sessions have `captured_content`
5. Dashboard — click any session in History tab, verify request/response bodies shown
6. Check health endpoint — `capture_mode: "all"` shown
