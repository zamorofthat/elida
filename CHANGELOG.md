# Changelog

All notable changes to ELIDA will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-02-18

### Added

- **Risk Ladder (Progressive Escalation)**
  - Cumulative risk score per session based on violation severity
  - Configurable thresholds: `warn` → `throttle` → `block` → `terminate`
  - Severity weights: info=1, warning=3, critical=10
  - New methods: `GetSessionRiskScore()`, `ShouldThrottle()`, `ShouldBlockByRisk()`, `ShouldTerminateByRisk()`

- **Token Burn Rate & Tool Tracking**
  - Track input/output tokens per session (OpenAI, Anthropic, Ollama formats)
  - Tool call counting with full history ("who called what")
  - Circuit breaker configuration for cost control
  - New file: `internal/proxy/tokens.go`

- **Immutable Event Stream**
  - Append-only audit log with SQLite storage
  - Event types: session_started, session_ended, violation_detected, tool_called, tokens_used
  - New API endpoints: `GET /control/events`, `GET /control/events/stats`, `GET /control/events/{session_id}`
  - New file: `internal/storage/events.go`

- **PII Redaction**
  - Automatic redaction of sensitive data in audit logs
  - Built-in patterns: email, SSN, credit card, phone, API keys, JWT, AWS keys, passwords, IP addresses
  - Custom pattern support via config
  - New file: `internal/redaction/redactor.go`

- **Chaos Suite (Benchmarking)**
  - 35 attack scenarios across 6 categories
  - Measures accuracy, sensitivity, specificity
  - New files: `test/chaos/scenarios.yaml`, `test/chaos/runner_test.go`, `scripts/chaos.sh`

- **New Policy Rules (Standard Preset)**
  - `prompt_injection_roleplay` — catches roleplay-based restriction bypasses
  - `bulk_data_extraction` — catches bulk user data/password dumps
  - `recursive_prompt` — catches exhaustive/brute-force prompts

- **Documentation**
  - New file: `docs/TELCO_CONTROLS.md` — comprehensive telco controls documentation
  - Updated `README.md` with telco controls section and new API endpoints
  - New file: `.dockerignore` — optimized Docker build context

### Fixed

- `prompt_injection_ignore` pattern now matches "disregard your system instructions"
- `privilege_escalation` rule now requires actual commands after `sudo` (fixes false positive on documentation)

### Changed

- Policy accuracy improved: 76% → 100% sensitivity, 90% → 100% specificity
- Session struct extended with token and tool tracking fields

### Performance

**Policy Accuracy (Chaos Suite):**

| Metric | Before | After |
|--------|--------|-------|
| Accuracy | 80% | 100% |
| Sensitivity | 76% | 100% |
| Specificity | 90% | 100% |
| False Positives | 1 | 0 |
| False Negatives | 6 | 0 |

**Request Latency by Preset:**

| Preset | Rules | Content Rules | Normal Req | Blocked Req |
|--------|-------|---------------|------------|-------------|
| Minimal | 3 | 0 | 53ms | N/A* |
| Standard | 21 | 14 | 46ms | <1ms |
| Strict | 38 | 30 | 61ms | <1ms |

*Minimal has no content rules (rate limiting only).

Blocked requests bypass the backend entirely, providing ~50-75x faster rejection for policy violations.

## [0.1.0+ci] - Unreleased

### Added

- **GitHub Actions CI Pipeline** (`.github/workflows/ci.yml`)
  - Lint job with golangci-lint
  - Security scanning: govulncheck, gosec, semgrep, trufflehog
  - Unit tests with race detection and coverage reporting
  - Integration tests with Redis
  - Cross-platform build matrix (linux/darwin/windows, amd64/arm64)

- **Linter Configuration** (`.golangci.yml`)
  - Enabled linters: errcheck, gosimple, govet, staticcheck, gofmt, bodyclose, unparam, noctx
  - Custom exclusion rules for test files

### Fixed

- Variable shadowing in `main.go`, `handler.go`, `storage_test.go`
- Unchecked error returns for `w.Write()` calls in HTTP handlers
- Response body not closed after `websocket.Dial()` calls
- Race condition in `TestVoiceSessionManager_Callbacks` using `atomic.Bool`
- Ineffectual assignment in `TestStreamingScanner_CrossChunkPattern`
- Empty if branches in `TestSQLiteStore_EmptyCapturedContentAndViolations`
- Missing test assertions for struct fields in telemetry and websocket tests

### Changed

- Removed deprecated `exportloopref` linter (replaced by Go 1.22 loopvar)

### Performance

Benchmark results (mode comparison):

| Metric | No Policy | Audit | Enforce |
|--------|-----------|-------|---------|
| Avg latency | 90ms | 92ms | 108ms |
| Blocked req latency | 84ms | 96ms | 45ms |
| Memory/session | 10KB | 12KB | 14KB |

## [0.1.0] - 2026-01-28

### Added

- Initial release
- HTTP/HTTPS reverse proxy for LLM backends
- Session tracking and management (create, kill, resume, terminate)
- Multi-backend routing (header, model, path-based)
- Policy engine with OWASP LLM Top 10 coverage
- WebSocket proxy for voice/real-time agents
- Voice session tracking with SIP-inspired lifecycle
- Transcript capture and post-session policy scanning
- Control API for session management
- Dashboard UI (Preact, embedded)
- Redis session store for horizontal scaling
- SQLite storage for session history
- OpenTelemetry integration for observability
- TLS/HTTPS support
