# Semantic injection detection: behaviour, configuration and limits

> The limitations list below will be folded into the root
> `SECURITY_LIMITATIONS.md` once PR #181 merges.

Semantic prompt-injection detection (`decision.*`) runs a packaged MiniLM
classifier over request content and its decoded representations. Its
behaviour and configuration rules (`require_inline`, the `max_concurrency`
split, when enforcement is refused, allowlisted tools, retained sessions) are
in [Configuration](configuration.md#semantic-injection-detection-decision).

## Telemetry

Semantic decisions are exported as metadata only. There is no switch for it:
the metadata is exported whenever OTEL or OCSF export is on.

**What is exported**

- **Per semantic rule**, the representative decision's event ID and 16
  metadata fields (`signal`, `probability`, `aux_probability`, `vetoed`,
  `model`, `model_version`, `model_checksum`, `threshold_set`, `decision_id`,
  `transform`, `transform_depth`, `window_start_byte`, `window_end_byte`,
  `coverage_complete`, `execution_mode`, `protection_scope`):
  - on the session span's `policy.violation` event as `event_id` and
    `semantic.*`;
  - on the violation log as `elida.violation.event_id` and `elida.semantic.*`;
  - in the OCSF Detection Finding as `unmapped."elida.event_id"` and
    `unmapped."elida.semantic"` (bare field names).
- **Per stored decision**, one OTEL log record (body `semantic decision:
  <rule>`), carrying `elida.semantic.rule`, `event_id`, `event_category`,
  `severity`, `source_role`, `message_index`, `evidence_only`,
  `would_contribute_points` and the same 16 `elida.semantic.*` fields, plus
  `gen_ai.conversation.id`, `gen_ai.provider.name` and
  `gen_ai.request.model`. An evidence-only decision is logged at Info severity.
  There is no OCSF finding per decision: findings stay one per rule.
- **On the session span**, `elida.semantic.decisions.count` (decisions
  exported as logs; set only when OTEL logs are on) and
  `elida.semantic.decisions.dropped` (decisions dropped past the per-session
  cap), set when the session has stored or dropped semantic decisions.

Decisions never raise `elida.violations.max_severity`; only violations feed
it, and evidence-only violations are excluded from it. Decisions stamped
before the ending session instance started (for example, on an entry for a
reused session ID that was not released) are not exported, the same rule
that applies to violations. A retained block or terminate entry holds no
decisions, so this is defense in depth.

**Volume.** The policy engine keeps at most 256 decisions per session, so a
session end emits at most 256 decision logs in addition to its violation
logs. Each carries 27 attributes, about 1 KB with typical values and under
3 KB with every identifier field at its 128-byte limit: roughly 0.3 MB, and
at most about 0.7 MB, of log attributes per session end before exporter
encoding. The session ID (`gen_ai.conversation.id`) and model name
(`gen_ai.request.model`) are not length-capped, so the estimate assumes they
are short.
Decisions past the cap are counted in `elida.semantic.decisions.dropped`, not
logged.

**What is never exported**

Request or response content of any kind: no message text, decoded payloads,
window text or matched text. Every exported string is restricted to
identifier-shaped values (letters, digits and `._:>+/=-`, at most 128 bytes)
and exported empty otherwise; an event ID that fails the check is left out.

## Temporal correlation

A single elevated window is weak evidence; a slow burn spreads an injection
across several messages so that no one message crosses the main threshold.
Temporal correlation (`correlation:` in `elida.yaml`, see
[configuration](configuration.md#temporal-correlation)) watches each
session's content-free violation events and records a match when a rule's
pattern completes. The shipped rule, `slow_burn_injection`, matches
`injection_elevated` evidence from three distinct messages within 15 minutes.

- **Audit-only by default.** Correlation is disabled by default, and the
  shipped rule is `mode: audit`: a match is recorded as an evidence-only
  `correlation.slow_burn_injection` violation and adds no risk. A rule set to
  `mode: enforce` contributes its severity to the risk ladder, except under
  `policy.mode: audit`, which caps it. Promoting a rule is a reserved
  decision.
- **Restart-only.** The block is not a settings key; a settings save keeps
  the engine and its state, and a change needs a restart.
- **Needs evidence to correlate.** `injection_elevated` events exist only
  when semantic detection runs in `audit` or `enforce`; in `shadow` the
  shipped rule sees nothing.
- **Sequences need their events in the history.** `shape: sequence` rules
  order steps by event timestamp, so a late asynchronous result still fills
  its step, but only while the earlier steps are still in the bounded
  history (see the budget below). No sequence rule ships by default.
- **One history budget per session.** `max_history_events` is shared by every
  category any rule wants. A flood of one wanted category can evict another
  category's evidence before its rule window has elapsed, so with several
  categories configured, a high-volume category is an evasion lever against a
  rule on a low-volume one. The shipped rule set has one category.
- **State is in memory.** History is lost on restart and when a session ends
  or is evicted by `max_sessions`.

## Limitations

Each entry says what is not caught or not guaranteed, and how it fails.

- **Encoded-payload scan is bounded to 4 runs.** Preprocessing decodes at most
  four base64/hex candidate runs per message. A decoy placed before the real
  payload can push the payload past the bound, and it is then not decoded (the
  original text is still scored).
- **Base64 glued to a word is not decoded.** A base64 run attached to a word
  with no delimiter fails to decode. This fails safe (the original is still
  scored), but the decoded representation is lost.
- **Short and odd-length hex is skipped.** Hex payloads shorter than 24 bytes,
  or of odd length, are not decoded.
- **`\xHH` escapes decode to code points, not bytes.** `\xHH` becomes the
  character U+00HH, not the raw byte 0xHH, so a multi-byte UTF-8 sequence
  written as `\x` escapes is not reassembled.
- **The `human_directed` veto cannot rescue its own probe.** Operator or
  runbook text addressed to a human can still score as injection (a false
  positive). Shadow-mode data will inform recalibration.
- **Calibration was fitted on upstream's int8 scoring.** ELIDA runs the model
  in float32, which shifts calibrated probabilities by up to about 0.064
  relative to that scoring (see the model card, "Calibration provenance").
  Recalibrating on the float32 path is pending; until then the thresholds are
  upstream's.
- **arm64 is async-only.** Accelerated inference kernels are gated to
  linux/amd64 built with `GOEXPERIMENT=simd`. Every other target, arm64
  included, reports `async_only`. On those builds the inline lane is never
  tried: every eligible window goes straight to the async lane in suspicion
  order (admission reason `capability_async_only`, bounded by
  `decision.max_async_windows`), so results arrive after the request was
  forwarded and cannot protect it, and `inline_completion_ratio` is 0.
  `decision.require_inline: true` refuses to start on such builds.
- **Content without an inline admission reason is scored async, last.** User
  and tool content that no `decision.inline_admission` rule admits (for
  example, a paraphrased injection with no lexical cue in an unelevated
  session) is still scored, on the async lane at the lowest priority: after
  every window of the request that was denied the inline lane for capacity.
  These windows wait in a separate LOW async queue (same `async_queue_size`)
  that workers serve only while the HIGH queue of admitted work is empty, so
  a flood of plain messages cannot crowd suspicious windows out; LOW overflow
  is counted in `async_low_dropped` and as `not_assessed`.
  Its result protects later activity only. When `decision.max_async_windows`
  is spent first, the windows left out are counted as the `not_assessed`
  coverage gap and the message is retried on the next request that carries
  it.
- **A long message can stay partly scored.** A message with more windows than
  the request's inline and async caps has some windows scored and the rest
  counted as `not_assessed`. Once any window of the message has answered, the
  message counts as assessed for the session, so its remaining windows are not
  revisited on later requests. The gap is visible in `coverage_gaps` and as
  `coverage_complete: false` on that message's decisions.
- **Raising `max_input_bytes` costs request latency.** Preprocessing runs on
  the request path and does not check the inline deadline. One 8 MiB message
  with `decision.preprocessing.max_input_bytes` / `max_analysis_bytes` at their
  ceilings (8 MiB / 32 MiB) measured 410 ms and 258 MB allocated end to end on
  darwin/arm64 (preprocessing alone 335 ms and 250 MB), against a 50 ms inline
  budget. At the defaults the same message costs 68 ms and 29 MB, because the
  input is truncated (an `input_truncated` gap). Raise the limits only with
  that cost in mind.
- **Async results need a live session binding.** The runner keeps a binding
  for every live session (removed only when the session ends), capped at
  65,536 live bindings as a hard safety limit; there is no session-manager
  maximum to derive it from. Past the cap a new session is assessed without
  per-session history and its async results are dropped. Every async result
  that finds no bound session (ended, or refused at the cap) is counted in
  `async_dropped_no_session` at `/control/decision` and logged at WARN at most
  once a minute, without content.
- **An inline window that misses the deadline is re-queued async.** On an
  inline build, a window whose inference does not finish inside
  `decision.inline_timeout` is handed to the async lane (bounded by
  `decision.max_async_windows`) instead of being dropped. Its result protects
  later activity only. The abandoned inline call may still be computing on its
  slot, so a miss can briefly cost two inferences.
- **One inference uses about 4.7 CPUs.** The embedded backend runs each
  inference over an intra-op worker pool, measured at about 4.7 CPUs of work
  per wall-clock second on an 8-core machine. Size `decision.max_concurrency`
  for that, not for one CPU per slot.
