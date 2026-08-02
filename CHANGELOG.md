# Changelog

All notable changes to ELIDA will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.18.0] - 2026-08-02

### Fixed

- Body redaction is now JSON-aware: only string values are scanned, numeric
  fields (`created`, `n_params`) are untouched, and captured bodies —
  including every SSE `data:` line — stay valid JSON. Previously ~59% of
  captured pairs were corrupted with ~zero true positives. Applied at all
  four redaction call sites: proxy capture (`CapturedContent` bodies), the
  OCSF event emitter payload, session-record redaction on capture
  (`cmd/elida`'s `redactRecord`), and the OTEL content log
  (`emitContentRecord` and `ExportSessionRecord`'s captured-content span
  events). (#feedback-10)
- Credit-card redaction requires a Luhn-valid number; phone redaction
  requires phone formatting (separators/parens); loopback and RFC1918 IPs
  are no longer redacted by default (`storage.redaction.redact_private_ips:
  true` restores the old behavior for public deployments).

## [0.17.0] - 2026-08-01

### Added

- `failover:` config section (`enabled`, `max_retries`, `retry_delay`,
  `fallback_order`, `preserve_model`) — disabled by default. (#feedback-9)
- `backends.<name>.model`: model id substituted in only when failover lands
  on that backend; normal routing is unaffected. (#feedback-9)
- `${VAR}` environment variable expansion in `backend`, `backends.<name>.url`,
  `backends.<name>.api_key`, `proxy.auth.api_key`, and `control.auth.api_key`
  — unset variables stay literal and log a warning. (#feedback-9)
- Auto provider keys: an empty `backends.<name>.api_key` is now looked up
  from `<NAME>_API_KEY`, then the conventional `OPENAI_API_KEY` /
  `ANTHROPIC_API_KEY` / `MISTRAL_API_KEY` for its type. (#feedback-9)

### Fixed

- Failover now forwards a compatible model id to the target backend, or
  skips that backend entirely, instead of forwarding the original model
  unchanged and getting a 400 from a cross-provider backend. (#feedback-8)
- Failover is now actually wired up from config at startup
  (`failover.enabled: true`) — previously the failover controller was
  constructible only in tests and had no effect on real traffic. Applies to
  non-streaming requests only; streaming (SSE) responses have no failover
  on error.
- When failover is attempted but exhausts every candidate (all skipped or
  failed), the client now receives a `502` with a JSON
  `{"error":"failover_exhausted",...}` body instead of the last attempted
  backend's raw response, which was indistinguishable from failover being
  disabled entirely.

## [0.16.0] - 2026-07-28

### Added

- Session identity can now derive from the OpenAI `user` field (default on)
  or a configurable `session.derive_from.body_path` — one conversation keeps
  one session across backend failover, and the kill-switch becomes
  per-conversation instead of per-host; see the security note in
  docs/configuration.md for multi-tenant deployments. (#feedback-4)
- `proxy.auth.trusted_networks`: CIDR allowlist whose direct peers skip
  inference-path auth, so un-keyed auxiliary agent calls (compression,
  title generation) work on trusted networks while the LAN still needs the
  key. Trust never consults X-Forwarded-For. (#feedback-4b)
- Startup warning when the inference proxy listens on a non-loopback
  address without authentication (symmetric with the control-API warning).

## [0.15.0] - 2026-07-26

### Added

- `policy.suppress_rules`: drop preset, custom, or generated rules by name.
- `observe: true` on a rule: flag + capture without feeding the risk ladder.
- `coding-agent` policy preset: structural rules enforce, content and
  statistical heuristics run in observe. (#feedback-3)

### Changed

- `compound_anomaly` description no longer claims an "exfiltration
  pattern" for a bare rate/entropy signal.

### Fixed

- Custom policy rules now **replace** same-named preset rules
  (local-overrides-default) instead of coexisting with them. (#feedback-1)
- `mode: audit` is now a true dry run: the risk ladder is clamped to
  observe/warn and can no longer block or terminate. (#feedback-2)

## [0.14.3] - 2026-07-06

### Security

- Dependencies: `picomatch` updated 4.0.3 → 4.0.4; GitHub Actions group
  bumped (12 updates); go-dependencies group bumped (17 updates).

## [0.14.2] - 2026-07-02

### Fixed

- Risk ladder config is now passed to the policy engine at startup —
  previously the configured thresholds were not applied to enforcement.

## [0.14.1] - 2026-06-07

### Changed

- `workflow_call` trigger added to `ci.yml` so the release workflow can
  reuse it.

### Security

- `golang.org/x/net` updated 0.54.0 → 0.55.0.

## [0.14.0] - 2026-05-30

### Added

- Circuit breaker defaults with token and tool-call limits.

## [0.13.1] - 2026-05-30

### Fixed

- `SetRedactor` is now guarded with a mutex in the instruction registry and
  telemetry paths (concurrency fix).

## [0.13.0] - 2026-05-25

### Changed

- Deployment configs hardened for container networking.

### Fixed

- Dockerfile hardened to prevent leaking private files into the image.

### Security

- Docker: `oven/bun` bumped 1.3.13-alpine → 1.3.14-alpine.

## [0.12.1] - 2026-05-25

### Security

- `semantic-release` upgraded to v25 to resolve npm vulnerabilities
  (`ip-address` XSS).

## [0.12.0] - 2026-05-25

### Added

- Secure-by-default configuration: startup security validation and
  loopback detection, localhost-only control API, and redaction on by
  default.
- Redaction wired into session, voice, flagged-capture, TTS, instruction
  file, and telemetry (OCSF/OTEL export) paths.
- Circuit breaker config wired into policy rule generation; risk ladder
  enabled by default with standard thresholds.
- Dashboard: expandable violation text across all views.
- Helm and Docker configs updated for secure defaults.

### Fixed

- `ELIDA_CONTROL_AUTH_ALLOW_INSECURE` env override added for local
  development.
- Dashboard now serves `index.html` for all SPA routes on reload.
- Tightened `curl|sh` regex to reduce false positives.

## [0.11.0] - 2026-05-24

### Added

- SBOM generation added to the release pipeline; deprecated archive-format
  usage fixed.

## [0.10.1] - 2026-05-17

### Security

- `golang.org/x/net` updated to v0.54.0 for GO-2026-4918; go-dependencies
  group bumped (3 updates).

## [0.10.0] - 2026-05-17

### Added

- Instruction file integrity: shape-based classification, path-marker
  extraction, instruction-specific rule scanner, in-memory hash registry
  with async persistence, SQLite storage adapter, REST endpoints for the
  instruction file registry, `tracked_types` dashboard setting, and
  `elida.yaml` config section. Enabled by default.

### Fixed

- SQL injection warning resolved — `ALTER TABLE` now uses a literal SQL
  string instead of interpolation.

### Security

- Dependencies: GitHub Actions group bumped (2 updates); `oven/bun` Docker
  image bumped 1.3.12-alpine → 1.3.13-alpine.

## [0.9.1] - 2026-05-03

### Fixed

- Resolved staticcheck SA5011 and govet shadow lint warnings.

## [0.9.0] - 2026-05-03

### Added

- Merkle tree SDR (Session Detail Record) integrity with telemetry export.

## [0.8.1] - 2026-05-03

### Changed

- Release Docker build moved to `release.yml` for semver tags.

### Fixed

- `semantic-release` now uses a PAT so it can trigger the release workflow.

## [0.8.0] - 2026-05-02

### Added

- Compound anomaly detection wired to OCSF 2004 events.

### Changed

- Docker build now triggers on GitHub release publish.

## [0.7.1] - 2026-05-02

### Fixed

- `CustomRules` merge handling fixed in the settings store, with new
  control API test coverage.

## [0.7.0] - 2026-05-02

### Added

- **Statistical Anomaly Detection**: Three new rule types for detecting session anomalies that evade static thresholds:
  - `rate_anomaly` — Poisson-based end-of-session retrospective check. Splits request timestamps into baseline/test windows, flags when observed rate is statistically abnormal (p-value threshold).
  - `content_entropy` — Shannon entropy of request/response content. Detects base64-encoded, compressed, or encrypted payloads that evade regex pattern matching. Strict preset only (code content can naturally reach 5.0-5.5).
  - `compound_anomaly` — Agent-first real-time detection using adaptive CUSUM + Shannon entropy compound scoring. Only alarms when both rate and entropy are elevated simultaneously, eliminating false positives from normal agent execution bursts. Uses time-weighted EMA for phase-tolerant rate tracking, CUSUM for evidence accumulation, and incremental byte-frequency entropy (O(1) per request, ~2KB per session).
- **Math Primitives** (`internal/policy/stats.go`): `poissonSurvival()` (log-space Poisson CDF) and `shannonEntropy()` (bits-per-byte) as standalone functions.
- **Per-Session Compound Detector** (`internal/policy/detector.go`): `SessionDetector` with adaptive CUSUM, incremental entropy, burst boundary detection, and ring buffer burst history. All operations O(1) per request.
- **CLI `-listen` Flag**: Override listen address from the command line (e.g. `elida -listen :8082`). Priority: CLI flag > `ELIDA_LISTEN` env > config file.
- **`ThresholdFloat` and `MinSamples` Rule Fields**: New optional fields on policy rules for probability thresholds (0-1), entropy thresholds (bits/byte), and minimum data points before evaluation.

### Changed

- Standard preset now includes `rate_anomaly` (p<0.01, warning) and `compound_anomaly` (threshold 0.15, flag) rules
- Strict preset tightens `rate_anomaly` to p<0.001/critical, `compound_anomaly` to threshold 0.10/block, and adds `content_entropy` at 5.5 bits/byte
- `StreamingScanner` accumulates full content for entropy evaluation on `Finalize()`, with burst-level reset on `Reset()`
- Content evaluation path feeds bytes to compound anomaly detectors for incremental entropy tracking
- Policy rule mapping in `cmd/elida/main.go` and `internal/control/api.go` now propagates `ThresholdFloat` and `MinSamples` fields
- Session history rebranded as "AI Session Detail Records" in docs.

### Fixed

- Lint issues from the stats/detector export refactor resolved; stats and
  detector APIs exported, tests moved to `test/unit`.

## [0.6.1] - 2026-04-30

### Security

- Dependencies: `undici` updated 7.24.0 → 7.24.1 (security fix); `modernc.org/sqlite`
  bumped in the go-dependencies group; `oven/bun` Docker image bumped
  1.3.11-alpine → 1.3.12-alpine; GitHub Actions group bumped (3 updates).

## [0.6.0] - 2026-04-26

### Added

- Dashboard redesign: KPI cards, requests chart, backend table, session
  detail modal with timeline, and an expandable sessions page with turn
  timeline.
- Behavioral fingerprint radar chart and risk score trend chart
  (exponential-decay visualization).
- Policy page (rules table, match counts, violations) and Tool Use page
  (risk-tinted pills, breakdown chart).

### Fixed

- `RecordToolCall` test calls updated to match the 4-arg signature; em-dash
  now renders correctly in the policy matches column; JSON tags added to
  policy `Config`; KPI request count fixed; sparklines added.

## [0.5.0] - 2026-04-22

### Added

- Automated `semantic-release` versioning in CI.

### Changed

- ** Behavioral Fingerprinting**: Session-level anomaly detection using Mahalanobis distance over 7 structural features (turn count, tool call ratio/entropy, token ratio, cadence median/CV, backend continuity). Baselines per session class (backend/model) with EWMA streaming updates.
- **Crash-Resilient Baseline Persistence**: Periodic flush via background ticker persists dirty baselines to SQLite every `flush_interval` (default 5m). Protects against data loss on hard crashes — at most one interval of baseline updates lost.
- **External Risk Points**: `AddExternalRiskPoints()` method on policy engine allows external scorers (M3-lite) to contribute risk points to the session risk ladder without creating violation events.
- **OCSF 2004 Anomaly Detection Findings**: Notable+ fingerprint anomalies emit Detection Finding events via OCSF native transport for SIEM correlation.
- **OCSF Native Transport**: Independent event delivery via stdout (JSONL for log shippers), webhook (with mTLS), and syslog (UDP/TCP/TCP+TLS). Not coupled to OTEL pipeline.
- **MCP Security Preset**: OWASP MCP Top 10 policy rules for Model Context Protocol security.
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

- Fixed unbounded failover recursion that could stack overflow when all backends fail (max 3 retries)
- Fixed Redis-backed deployments showing stale active sessions — session completed state now persisted to store
- Fixed dashboard CSS variable references (`--warning` → `--color-warning`, `--border` → `--border-color`)
- Fixed nil channel block in session `Snapshot()` — closed `killChan` now initialized
- Fixed concurrent dashboard `useEffect` leaks — added `AbortController` to all fetch calls
- Fixed SQLite stats queries — `GetStats`, `GetVoiceStats`, `GetTTSStats`, `GetEventStats` wrapped in read transactions for consistent snapshots
- Removed dead `parseNDJSONContent` and deduplicated streaming response log paths
- `P2Quantile` initial buffer now preserved across JSON round-trip.

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
- Dependencies: GitHub Actions group bumped (2 updates, then 4 updates);
  go-dependencies group bumped across 1 directory (10 updates).

## [0.4.3] - 2026-04-14

### Added

- MCP security preset: OWASP MCP Top 10 policy rules for Model Context
  Protocol security.
- MkDocs Material documentation site (dark/light mode, search, GitHub
  Pages auto-deploy); new integrations page (LiteLLM, Portkey, Aperture,
  Oso, OTEL/SIEM).
- Blog section with first post.

### Changed

- **Per-Message Content Scanning**: Policy engine now scans each message individually instead of concatenating all content into a flat string. Each violation carries `source_role` (user/assistant/system/tool), `message_index`, and `source_content` for precise attribution.
- **Anthropic System Prompt Parsing**: Top-level `system` field from Anthropic API requests is now parsed and hash-cached. Previously only OpenAI-style `role: "system"` messages were handled.
- **Source-Weighted Risk Scoring**: Risk scores now factor in where the violation was found. User input scores full weight (1.0x), tool results 0.8x, assistant messages 0.2x, system prompts 0.1x. Reduces false positive impact from model output echoing safety patterns.
- **Exponential Decay on Risk Scores**: Violation contributions decay over time using `e^(-λt)` formula (λ=0.002, half-life ~5.8 minutes). Old violations naturally fade instead of permanently inflating risk scores.
- **Effective Severity**: Each violation now includes an `effective_severity` field that combines the rule severity with source-role weighting. A critical rule triggered by an assistant echo downgrades to warning or info.
- **SIEM-Friendly Structured Violations**: Violations include `event_category` (prompt_injection, data_exfil, rate_limit, etc.) and `framework_ref` (OWASP-LLM01, ELIDA-FIREWALL, etc.) for SIEM correlation rules.
- **OTEL Capture Modes**: `telemetry.capture_content` changed from boolean to three-mode string: `"none"` (default), `"flagged"` (only policy-flagged sessions), `"all"` (full audit). Enables targeted content shipping to SIEM.
- **Dashboard Source Attribution**: Flagged session details now show source role badges (color-coded by role), message index, framework reference tags, and effective severity instead of raw rule severity.
- `telemetry.capture_content` config field changed from `bool` to `string` (`"none"`, `"flagged"`, `"all"`)
- `calculateMaxSeverity` now uses effective severity (source-weighted) instead of raw rule severity
- Risk score calculation uses per-event time-series with decay instead of simple count × weight formula
- Policy violation logs now include `source_role`, `message_index`, `effective_severity`, `event_category`, `framework_ref`, and `source_content` fields
- GoReleaser pipeline: added SLSA provenance attestation.

### Fixed

- Fixed Anthropic API system prompt not being parsed — top-level `system` field was completely ignored by the content scanner
- Fixed false positive storm from Claude Code system prompt — "ignore all previous instructions" in safety text triggered `prompt_injection_ignore_request` on every request
- System prompt hash caching now works for both Anthropic (top-level field) and OpenAI (role message) formats

### Security

- Dependencies: `go.opentelemetry.io/otel/sdk` updated 1.42.0 → 1.43.0;
  `google.golang.org/grpc` updated 1.79.2 → 1.79.3; `picomatch` updated
  4.0.3 → 4.0.4; `undici` updated 7.23.0 → 7.24.0; `modernc.org/sqlite`
  bumped (go-dependencies group, multiple rounds); GitHub Actions group
  bumped (7 and 8 updates); `oven/bun` Docker image bumped 1.3.10-alpine →
  1.3.11-alpine.

## [0.4.2] - 2026-03-14

### Changed

- Deduplicated streaming chunk reconstruction into `joinChunks`; replaced
  constructor telescoping chains with a functional options pattern.

### Fixed

- Removed duplicate response reconstruction and dead `parseNDJSONContent`.
- Dashboard polling reduced from 2s to 5s, and skipped when the tab is
  hidden.
- SQLite `MaxOpenConns=1` set to prevent "database is locked" errors.
- Added X-Forwarded-For/X-Real-IP support for session identity.
- Initialized closed `killChan` in `Snapshot()`.
- Streaming request detection now uses JSON unmarshal instead of string
  matching.
- Streaming tests moved to `test/unit`; control API route patterns fixed.

## [0.4.1] - 2026-03-14

### Security

- Hardened control API auth, request limits, and CORS.

### Fixed

- Eliminated a session race window and an async goroutine stale-pointer
  bug.

## [0.4.0] - 2026-03-13

### Added

- Tool call policy: `tool_blocked` and `tool_argument_pattern` rule types;
  tool call argument capture with a generic provider fallback.
- Graceful shutdown: session drain on SIGINT/SIGTERM (T11).

### Changed

- Removed redundant CI workflow YAMLs.

## [0.3.1] - 2026-03-13

### Added

- Frontend authentication: login page, `apiFetch` wrapper with auth header
  and 401 handling, and auth gating for the dashboard.
- Session history dashboard pagination.
- Helm `deployment.yaml` for Kubernetes deployment.

### Changed

- Dynamic versioning via `git describe` in the Makefile.
- Extracted the `main()` "god function" into focused init methods.
- CI lint job now runs `go mod tidy` to catch dependency drift.

### Fixed

- Broken dashboard CSS variable references.
- Duplicate `useEffect` hooks merged; `AbortController` added to dashboard
  fetches.
- SQLite stats queries wrapped in read transactions for consistent
  snapshots.
- Missing HTTP transport timeouts added.
- Silent `io.ReadAll` error in request body capture fixed.
- Regex compilation moved off the write lock; unbounded risk score bug
  fixed.
- Race condition in session kill check-and-delete fixed.
- Auth-disabled startup warning added.
- Session state now persisted on completion.
- Failover recursion depth limit added.
- Dashboard login bug fixed.

## [0.3.0] - 2026-03-12

### Added

- OTEL log and metric providers wired into the proxy (GenAI
  semantic-convention histograms for tokens/duration).
- `CaptureContent`/`MaxBodySize` fields on `TelemetryConfig`.

### Changed

- Version is now injected at build time via `ldflags`/Docker build args
  instead of being hardcoded.
- Prompt injection rules split by request/response direction, with
  critical severity on the request side.
- Policy enforcement enabled by default in settings.

### Fixed

- Tool/content allowlist config and regex bug fixes.
- CI coverage reporting: added `-coverpkg` for accurate coverage.

## [0.2.3] - 2026-03-11

### Added

- Proxy-level API key authentication for keyless clients (injects backend
  API keys; strips `X-Elida-API-Key` before forwarding).
- Trusted tags to skip scanning of system-injected content.

### Changed

- Control API auth uses constant-time comparison.

### Fixed

- Added TLS credentials for OTEL gRPC export.
- Fixed the SLSA release workflow (matrix output aggregation; simplified
  to a single build job).

### Security

- Dependencies: GitHub Actions group bumped (10 updates).

## [0.2.2] - 2026-03-10

### Added

- CODEOWNERS file for branch protection rules.
- OpenSSF Scorecard workflow, SLSA provenance step, and SBOM generation
  (with `-licenses` flag) in CI.
- SECURITY.md and Dependabot configuration.

### Changed

- Go toolchain and CI images bumped to Go 1.26; linter/gosec versions
  updated.
- CI SARIF upload from gosec and semgrep for Scorecard SAST detection;
  workflow permissions and SSRF exclusions tightened.

### Fixed

- Patched a security vulnerability in `settings.go`.

### Security

- Dependencies: `github.com/redis/go-redis/v9`, `modernc.org/sqlite`, OTEL
  exporters (`stdouttrace`, `otlptracegrpc`), `preact`,
  `@preact/preset-vite`, `vite` bumped; GitHub Actions (`checkout`,
  `download-artifact`, `build-push-action`, `login-action`,
  `action-gh-release`) and the `alpine` base image updated.

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
