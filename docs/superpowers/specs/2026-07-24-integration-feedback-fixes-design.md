# Design: Integration-feedback fixes (8 high-impact items)

**Date:** 2026-07-24
**Source:** `elida-feedback.md` (integration feedback from the Hermes/llama.cpp local-first stack) + the cloud-fallback doc from the same project.
**Scope:** the 8 high-impact items — #1, #2, #3, #4, #4b, #8, #9, #10.
**Structure:** 4 subsystem branches, one focused PR each, landing in order.

All feedback claims were verified against the source before this design:

| # | Claim | Verified at |
|---|---|---|
| 1 | Preset + custom rules merged, not deduped | `internal/config/config.go:1009` — `append(presetRules, c.Policy.Rules...)` |
| 2 | Risk ladder blocks independent of audit mode | `internal/policy/policy.go:285-286` — `{30: Block}, {50: Terminate}` defaults |
| 4 | Session = hash(clientIP + backend + hour) | `internal/session/manager.go:154-162, 246` |
| 4b | Proxy auth is all-or-nothing per request | `internal/proxy/proxy.go:1531-1535` |
| 8 | Failover forwards model id unchanged (small remap table) | `internal/proxy/rehydrate.go:285-286`, `failover.go` |
| 9 | No `${ENV}` expansion; only `ELIDA_*` overrides | `internal/config/config.go:341, 543` |
| 10 | Content-blind body redactor (regex over raw string) | `internal/redaction/redactor.go:29-95` |

---

## Branch 1: `fix/policy-layering` — items #1, #2, #3

### #1 Local-overrides-default rule merge

In `ApplyPolicyPreset()` (`internal/config/config.go:989`), replace the blind
`append` with a merge keyed by rule name:

1. Build the preset rule list.
2. For each custom rule: if its `name` matches a preset rule, the custom rule
   **replaces** the preset rule (Splunk/Cribl/Grafana local-vs-default
   semantics). Otherwise it is appended.
3. New `policy.suppress_rules: [name, ...]` list drops preset rules by name
   without redefining them.
4. Startup log lists which preset rules were overridden or disabled, so the
   layering is visible.

### #2 Audit mode gates the risk ladder

In `internal/policy/policy.go`: when `mode: audit`, risk-ladder threshold
actions are clamped to `observe`/`warn`. The score is still computed, logged,
and visible in the dashboard — but audit mode can never throttle, block, or
terminate. No new config knob.

### #3 `coding-agent` preset

New preset alongside `minimal`/`standard`:

- **Enforced (deterministic, structural):** dangerous tool names
  (`exec_*/shell_*/rm_*/sudo_*/eval_*`), credential-exfil tool calls,
  tool-call circuit breaker with `max_tool_fanout: 100`.
- **Shadow (new per-rule `shadow: true` field):** shell/agency content
  patterns, PII regex, `rate_anomaly`, `compound_anomaly`. Shadow = flag +
  capture, but contributes **0** to the risk ladder.
- Reword anomaly rule messages: "elevated rate/entropy signal", not
  "agent exfiltration pattern" — a heuristic must not read as a confirmed breach.

Depends on #1 (users tune the preset via override semantics).

---

## Branch 2: `fix/session-identity-auth` — items #4, #4b

### #4 Session identity from the request body

In `internal/session/manager.go`, derivation precedence becomes:

1. `X-Session-ID` header (existing, still wins)
2. **OpenAI `user` field** from the request body (new; on by default)
3. Configurable JSON path — `session.derive_from.body_path` (new)
4. Existing IP-hash fallback (unchanged behavior for clients sending nothing)

The derived ID is computed **before** backend is mixed in: one conversation
that fails over local → nemotron keeps one session. Backend becomes a session
*attribute* (`backends_used`), not part of its identity. This fixes both
failure modes measured in the feedback: unrelated conversations merging
within a backend-hour, and one conversation splitting across backends.

### #4b `proxy.auth.trusted_networks`

CIDR allowlist on the inference-path auth:

```yaml
proxy:
  auth:
    enabled: true
    api_key: ...
    trusted_networks: [127.0.0.1/32, ::1/128, 172.16.0.0/12]
```

- Requests from a listed CIDR skip the Bearer check; everything else needs it.
- `X-Forwarded-For` honored only from a trusted hop.
- Docs call out the Docker-gateway gotcha: the container sees the client as
  the bridge gateway (e.g. `172.17.0.1`), so a bare-loopback exemption never
  fires — include the bridge subnet.
- Also: startup warning when the proxy runs unauthenticated (symmetry with
  the existing control-API warning; same code path).

---

## Branch 3: `fix/backend-config` — items #8, #9

### #8 Failover model rewrite

Add `model:` to `BackendConfig` (`internal/config/config.go:216`) — the model
id to send when failover *lands on* this backend. Rewrite order on failover:

1. Explicit `backends.<name>.model`
2. Existing `SelectCompatibleModel` table (`rehydrate.go:286`)
3. **Fail loudly** — log + skip the backend — instead of forwarding a doomed
   id (e.g. `gemma` → Mistral → 400).

Normal (non-failover) routing is unchanged.

### #9 Secrets out of the committed config

Two mechanisms:

- **(a) `${ENV}` expansion** in config string fields at load
  (`config.go:341`). Targeted: only `${IDENTIFIER}` form is expanded, so
  regex `$` anchors in policy rule patterns are untouched. Unset variable →
  startup warning naming the variable.
- **(b) Auto-read provider keys:** when a backend's `api_key` is empty,
  read the conventional env var for its `type` (`MISTRAL_API_KEY`,
  `NVIDIA_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`) — making
  `.env.example`'s implied promise true.

---

## Branch 4: `fix/redaction` — item #10

Rewrite `internal/redaction/redactor.go` to be **JSON-aware**:

- Parse the body; for SSE, parse each `data:` line individually.
- Walk the structure; apply patterns **only to string values** — never keys,
  never numeric fields (`created`, `n_params`, `size`).
- Re-serialize — the stored record is guaranteed valid JSON (replayable).
- Non-JSON bodies fall back to the current raw-string path.

Pattern tightening:

- **CC:** Luhn check required.
- **Phone:** format context required (separators / length bounds), not any
  10–13 digit run.
- **IP:** skip loopback/RFC1918 by default; opt-in flag to include them.

Acceptance test built from the measured failures: a body with
`"created": 1753…`, an SSE stream, and `--host 127.0.0.1` must come out
untouched and parseable, while a real phone/CC/SSN in message *content* still
redacts.

---

## Cross-cutting

- **Order:** Branch 1 first (#3 depends on #1). Branches 2–4 are independent
  of each other and could proceed in parallel after that.
- **Tests (TDD):** each item starts with a failing test reproducing the
  reported behavior (e.g. "audit mode returns 403 after 30 flag-points" must
  pass through after #2).
- **Docs:** each branch updates `docs/policy-rules-reference.md` / config
  docs for what it touches (partial credit on feedback #7);
  CHANGELOG entry per item referencing the feedback number.
- **Compatibility:** defaults preserve current behavior everywhere except
  where current behavior *is* the bug (#2 ladder-in-audit, #10 corrupting
  redactor).

## Out of scope (this effort)

Feedback items not included: #5 (health-endpoint auth exemption), #6 (README
quickstart image), #7 as a standalone (doc generation from rule structs), and
the minor/polish items. Each is a good later candidate.
