# Changelog

All notable changes to ELIDA will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- ** Behavioral Fingerprinting**: Session-level anomaly detection using Mahalanobis distance over 7 structural features (turn count, tool call ratio/entropy, token ratio, cadence median/CV, backend continuity). Baselines per session class (backend/model) with EWMA streaming updates.
- **Crash-Resilient Baseline Persistence**: Periodic flush via background ticker persists dirty baselines to SQLite every `flush_interval` (default 5m). Protects against data loss on hard crashes — at most one interval of baseline updates lost.
- **External Risk Points**: `AddExternalRiskPoints()` method on policy engine allows external scorers (M3-lite) to contribute risk points to the session risk ladder without creating violation events.
- **OCSF 2004 Anomaly Detection Findings**: Notable+ fingerprint anomalies emit Detection Finding events via OCSF native transport for SIEM correlation.
- **OCSF Native Transport**: Independent event delivery via stdout (JSONL for log shippers), webhook (with mTLS), and syslog (UDP/TCP/TCP+TLS). Not coupled to OTEL pipeline.
- **MCP Security Preset**: OWASP MCP Top 10 policy rules for Model Context Protocol security.
- **Per-Message Content Scanning**: Policy engine now scans each message individually instead of concatenating all content into a flat string. Each violation carries `source_role` (user/assistant/system/tool), `message_index`, and `source_content` for precise attribution.
- **Anthropic System Prompt Parsing**: Top-level `system` field from Anthropic API requests is now parsed and hash-cached. Previously only OpenAI-style `role: "system"` messages were handled.
- **Source-Weighted Risk Scoring**: Risk scores now factor in where the violation was found. User input scores full weight (1.0x), tool results 0.8x, assistant messages 0.2x, system prompts 0.1x. Reduces false positive impact from model output echoing safety patterns.
- **Exponential Decay on Risk Scores**: Violation contributions decay over time using `e^(-λt)` formula (λ=0.002, half-life ~5.8 minutes). Old violations naturally fade instead of permanently inflating risk scores.
- **Effective Severity**: Each violation now includes an `effective_severity` field that combines the rule severity with source-role weighting. A critical rule triggered by an assistant echo downgrades to warning or info.
- **SIEM-Friendly Structured Violations**: Violations include `event_category` (prompt_injection, data_exfil, rate_limit, etc.) and `framework_ref` (OWASP-LLM01, ELIDA-FIREWALL, etc.) for SIEM correlation rules.
- **OTEL Capture Modes**: `telemetry.capture_content` changed from boolean to three-mode string: `"none"` (default), `"flagged"` (only policy-flagged sessions), `"all"` (full audit). Enables targeted content shipping to SIEM.
- **Dashboard Source Attribution**: Flagged session details now show source role badges (color-coded by role), message index, framework reference tags, and effective severity instead of raw rule severity.
- **Tool Call Policy Rules**: `tool_blocked` and `tool_argument_pattern` rule types for blocking specific tools or scanning tool call arguments.
- **Trusted Tags**: XML-style tags (e.g. `<system-reminder>`) can be stripped before scanning to prevent false positives on framework-injected content.
- **Tool Allowlist**: Requests containing only allowlisted tools skip request-side content scanning.
- **Proxy-Level API Key Authentication**: Optional API key injection for keyless client support.
- **Frontend Authentication**: Login page and auth gating for dashboard UI.
- **Graceful Shutdown**: Session drain on SIGINT/SIGTERM — invokes session end callbacks for all active sessions before exiting.
- **Dashboard Pagination**: Session history list supports pagination.
- **GoReleaser Pipeline**: Cross-platform builds with SLSA provenance attestation.
- **OTEL Logs & GenAI Metrics**: Security events (violations, blocks, killed sessions) emit structured OTEL log records. Token usage and operation duration recorded as histograms per GenAI semantic conventions.
- **MkDocs Documentation Site**: Material theme with dark/light mode, search, auto-deploy to GitHub Pages. New integrations page covering LiteLLM, Portkey, Aperture, Oso, OTEL/SIEM.
- **Helm Deployment Template**: Health checks, config mount, Redis integration for Kubernetes deployments.
- **Startup Auth Warning**: Control API logs warning when authentication is disabled.

### Changed

- `telemetry.capture_content` config field changed from `bool` to `string` (`"none"`, `"flagged"`, `"all"`)
- `calculateMaxSeverity` now uses effective severity (source-weighted) instead of raw rule severity
- Risk score calculation uses per-event time-series with decay instead of simple count × weight formula
- Policy violation logs now include `source_role`, `message_index`, `effective_severity`, `event_category`, `framework_ref`, and `source_content` fields
- Fingerprinting enabled by default in shadow mode (ingest-only, no scoring until baselines are warm)
- Extracted 575-line `main()` god function into `app` struct with focused init methods
- Control API and proxy constructors use functional options pattern instead of telescoping chains
- Streaming request detection uses JSON unmarshal instead of fragile string matching
- Dashboard polling reduced from 2s to 5s, polls skip when tab is hidden
- CORS restricted to same-origin via hostname comparison (was wildcard `*`)
- SQLite `MaxOpenConns=1` to prevent "database is locked" under concurrent load
- CI lint job now includes `go mod tidy` check to catch dependency drift
- Dynamic version via `git describe` instead of hardcoded `0.1.0`
- CI workflow permissions tightened from `read-all` to workflow-level scopes

### Fixed

- Fixed Anthropic API system prompt not being parsed — top-level `system` field was completely ignored by the content scanner
- Fixed false positive storm from Claude Code system prompt — "ignore all previous instructions" in safety text triggered `prompt_injection_ignore_request` on every request
- System prompt hash caching now works for both Anthropic (top-level field) and OpenAI (role message) formats
- Fixed unbounded failover recursion that could stack overflow when all backends fail (max 3 retries)
- Fixed Redis-backed deployments showing stale active sessions — session completed state now persisted to store
- Fixed dashboard CSS variable references (`--warning` → `--color-warning`, `--border` → `--border-color`)
- Fixed nil channel block in session `Snapshot()` — closed `killChan` now initialized
- Fixed concurrent dashboard `useEffect` leaks — added `AbortController` to all fetch calls
- Fixed SQLite stats queries — `GetStats`, `GetVoiceStats`, `GetTTSStats`, `GetEventStats` wrapped in read transactions for consistent snapshots
- Removed dead `parseNDJSONContent` and deduplicated streaming response log paths

### Security

- Control API auth uses constant-time comparison to prevent timing side-channel attacks
- Request body capped at 10MB (proxy) and 1MB (control API) to prevent OOM via large payloads
- Risk ladder now enforced in request path — `ShouldBlockByRisk` and `ShouldThrottle` wired in (was observe-only)
- Async `asyncScanResponse` receives session snapshot instead of live pointer (race fix)
- `persistFlaggedSession` uses `sess.Snapshot()` instead of unlocked field reads (race fix)
- `TouchAndRecord()` batches session updates under single lock (race fix)
- Trusted tag regex pre-compiled at startup instead of per-request
- X-Forwarded-For/X-Real-IP support so NAT/shared-IP clients get distinct sessions
- GoReleaser pipeline with SLSA Level 3 provenance attestation for supply chain security
- Aikido security scanner integrated into CI

## [0.2.1] - 2026-02-22

### Added

- **Settings UI Page**
  - Full settings management in dashboard (Policy, Capture, Failover sections)
  - Custom rules editor with add/edit/remove functionality
  - RE2 regex pattern support for content matching rules
  - Settings hint explaining custom rules append to preset rules

- **Dynamic Settings Reload (Hot-Reload)**
  - Policy engine reloads configuration without restart
  - VS Code-style layered settings: `elida.yaml` → ENV vars → `settings.yaml` (UI)
  - New policy engine method: `ReloadConfig(cfg Config)`

- **Unified Settings Hierarchy**
  - `NewSettingsStoreFromConfig()` initializes defaults from loaded config
  - Local overrides saved to `configs/settings.yaml` (YAML format)
  - Settings endpoints: GET/PUT/DELETE `/control/settings`

- **Dashboard Improvements**
  - ELIDA favicon (purple brand icon)
  - Settings navigation in sidebar

### Fixed

- CORS headers now include PUT/DELETE methods for settings API
- Settings stored in `configs/` directory alongside `elida.yaml`

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
