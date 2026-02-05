# Changelog

All notable changes to ELIDA will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
