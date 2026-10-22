# Semantic injection detection: behaviour, configuration and limits

> Residual risk (what this detector cannot see, and how it fails) is under
> [Residual risk](#residual-risk) below; the per-feature limitations follow
> it.

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
- **Per correlation match** (a `correlation.<rule>` violation), the match ID
  as the event ID and nine evidence fields (`match_id`, `rule`, `shape`,
  `mode`, `event_count`, `categories`, `first_event`, `last_event`,
  `event_ids`):
  - on the span event as `event_id` and `correlation.*`;
  - on the violation log as `elida.violation.event_id` and
    `elida.correlation.*`;
  - in the OCSF finding as `unmapped."elida.event_id"` and
    `unmapped."elida.correlation"` (bare field names).

  `event_ids` carries the first 32 contributing IDs and `categories` the
  first 8 distinct categories, in order of first appearance (both hard caps,
  equal to the engine's own ceilings today); `event_count` is the full
  count. Times are RFC3339Nano in UTC. Only the match retained on the
  session at its end is exported, one per rule (see
  [Residual risk](#residual-risk)).
- **Per shadow correlation match**, one OTEL log at Info severity (body
  `shadow correlation match: <rule> (calibration only, never a violation)`)
  carrying the same nine `elida.correlation.*` fields plus
  `elida.correlation.shadow=true` and `elida.correlation.severity`, and the
  three `gen_ai.*` session attributes; at most 50 per session end, one per
  `correlation_shadow` entry of the ending session instance. There is no
  OCSF finding: a shadow match is calibration data. `semantic_shadow`
  decisions are not exported.
- **On the session span**, `elida.semantic.decisions.count` (decisions
  exported as logs; set only when OTEL logs are on) and
  `elida.semantic.decisions.dropped` (decisions dropped past the per-session
  cap), set when the session has stored or dropped semantic decisions; and
  `elida.correlation.shadow.count` (shadow correlation matches exported as
  logs; set only when OTEL logs are on and the session has any).

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
logged. A correlation violation adds at most 9 attributes: about 1.3 KB with
32 event IDs at their usual 35-byte width, about 6 KB with every string at
the 128-byte cap. A session end emits at most 50 shadow correlation logs of
14 attributes each.

**What is never exported**

Request or response content of any kind: no message text, decoded payloads,
window text or matched text. Every exported string is restricted to
identifier-shaped values (letters, digits and `._:>+/=-`, at most 128 bytes)
and exported empty otherwise; an event ID that fails the check is left out.

## Temporal correlation

A single elevated window is weak evidence. A slow burn spreads an injection
across several messages so that no one message crosses the main threshold.
Temporal correlation watches each session's content-free events and records
a match when a rule's pattern completes. It is configured in the top-level
`correlation:` block of `elida.yaml` (see
[configuration](configuration.md#temporal-correlation)). The shipped rule,
`slow_burn_injection`, matches `injection_elevated` evidence from three
distinct messages within 15 minutes.

**How rules work.** Correlation reads event metadata only: event ID,
category, source role and message index, and timestamp. It never reads
content. There are two rule shapes:

- `repeated`: at least `count` events of one `category` within `window`.
  With `count_by: message`, several events from one message count once.
  Those events are its windows and its decoded representations, and a
  message is identified by source role and message index. The default
  rule uses this, so one long message is not a slow burn.
- `sequence`: 2 to 8 categories in order within `window`. Order is event
  time, not arrival, so an async semantic result that lands late still
  fills its step.

For both shapes:

- Each event contributes to at most one match of a rule.
- After a match, `cooldown` (default: the rule's `window`) suppresses
  another match of that rule in that session.
- A match has a stable match ID (`cm_…`) and carries IDs, categories and
  timestamps, never content.

**Audit-only by default.** Correlation is disabled by default
(`correlation.enabled: false`). The shipped rule is `mode: audit`. In
`audit` and `enforce`, a match is recorded as an evidence-only
`correlation.slow_burn_injection` violation, with event category
`correlation_match` and the contributing event IDs under `correlation` on
`GET /control/flagged/{id}`. It adds no risk. A rule set to `mode: enforce`
contributes its severity to the existing risk ladder, except under
`policy.mode: audit`, which caps it to evidence. Promoting a rule is a
reserved decision, and startup logs a WARN for every enforce rule.

**Built-in chains (`builtin_chains`).** `correlation.builtin_chains: true`
adds two audit-only sequence rules, `builtin_credential_exfiltration`
(`credential_discovery` then `external_transmission`) and
`builtin_sensitive_exfiltration` (`sensitive_access` then
`external_transmission`), both within 15 minutes, together with the tool
categories they need. It is off by default and is a heuristic: tool names
and argument shapes vary by agent framework, and its globs and patterns are
listed in full under
[configuration](configuration.md#built-in-chains) so an operator can judge
them. Built-in taxonomy entries are appended after the configured
`tool_categories`, and a built-in rule is skipped when a configured rule has
its name, so a rule may be overridden by redefining it under that name, for
example with another window or, as a reserved decision, `mode: enforce` to
promote it. A match is recorded as an evidence-only
`correlation.builtin_credential_exfiltration` (or
`..._sensitive_exfiltration`) violation and adds no risk.

**Restart-only, outside Settings.** The `correlation:` block is not part of
`settings.yaml` or `/control/settings`. A settings save keeps the
correlation engine and its state, and changing the block needs a restart.
Policy correlation (violation events and tool activity) needs
`policy.enabled: true`. Shadow correlation of semantic decisions (below)
does not: with `policy.enabled: false`, decision is capped to shadow, the
shadow correlator still runs, and startup logs a WARN that policy
correlation is inactive while shadow correlation is active.

At startup ELIDA logs one `temporal correlation enabled` line. It carries:

- the rule count, the shapes, and the mode of each rule;
- the limits;
- whether policy correlation runs and whether the semantic runner has a
  shadow correlator;
- whether the built-in chains are on (`builtin_chains`);
- the taxonomy size and its warnings;
- the tool-lane bounds.

The line contains no patterns and no content.

**Shadow-mode correlation (`correlation_shadow`).** In the default
`decision.mode: shadow`, semantic detection records no violations, so the
policy correlator has no `injection_elevated` evidence to read. A separate
shadow correlator takes its place:

- It is built from the same `correlation` rules and limits, as its own
  engine, never the policy engine's.
- It is fed every shadow decision in the elevated band: at or above
  `decision.elevated_threshold`, below the main threshold, not vetoed.
- A match is recorded as a `correlation_shadow` entry on the session,
  returned by `GET /control/sessions/{id}` next to `semantic_shadow`
  (newest first, at most 50; past 50 the oldest entry is dropped). The
  entry has the rule, match ID, shape, severity, mode, the contributing
  DecisionIDs, categories and times.
- At session end the entries are persisted (with `storage.enabled`) in
  the `correlation_shadow` column of the stored session and served by
  `GET /control/history/{id}` (the history list leaves them out), and,
  when OTEL logs are on, each is exported as one OTEL log (see
  [Telemetry](#telemetry)). Across reuse of a session ID the earlier
  sessions' entries are carried behind the current session's, deduplicated
  by match ID, 50 in total. Carrying does not recover entries an earlier
  session had already dropped past its own 50.
- It is calibration data: never a violation, never risk, and the traffic
  stays byte-identical.

One decision feeds at most one correlator. In effective shadow mode it goes
to the shadow correlator; in `audit` and `enforce` it goes to the policy
correlator as a violation event. Shadow state belongs to one session
instance:

- It is forgotten when the session ends (the persisted entries remain).
- A session that reuses an ended session's ID starts empty.
- A request that arrives on a session that has already ended is not
  correlated.

The shadow correlator needs no shutdown drain. It is updated synchronously
when a decision is handled, owns no goroutines or queues, and its in-memory
state ends with the process. It runs outside the semantic runner's
session-registry lock, so a slow correlation on one session never delays
another session's assessment. A match computed while its session ended (or
its ID was reused) is dropped, counted in `shadow_matches_dropped` at
`GET /control/decision`, and that instance's state is forgotten. Accepting
a match onto its session and releasing the session at its end are
synchronized on the runner's session-registry lock (the correlation itself
still runs outside it), so once the session-end path has released a
session, no further shadow match can be written to it: the record persisted
at session end holds the matches accepted before the release (the newest
50 of them), and an in-flight match rejected at session end or shutdown is
counted in `shadow_matches_dropped` and absent from the persisted `correlation_shadow`.

**Tool activity (`tool_categories`).** `correlation.tool_categories` maps
tool calls returned by the backend to categories, so rules can use them.
For example, `sequence: [credential_discovery, external_transmission]`.

- `tools` are case-insensitive globs over the whole tool name. `*` and `?`
  match any character, including `/`.
- `argument_patterns` are case-insensitive RE2 regexes. They are matched
  against the first `max_argument_bytes` of the call's arguments (default
  16 KiB, at most 64 KiB).
- Categorization runs off the request path, on a bounded tool lane: 4
  workers, 256 batches queued per worker, 64 batches per session, 32 MiB
  of queued arguments, 128 calls per batch, and 256 remembered provider
  call IDs per session (later calls use the anonymous event form and still
  count). What overflows is dropped and counted. A batch's events (calls x
  matching categories) are observed in chunks of 256, so a batch that falls
  into several categories loses nothing.
- Arguments are scanned and then dropped. They are never stored or
  logged.
- A categorized call that completes no match records nothing.

**Taxonomy cost model.** RE2 never backtracks, but a match costs time
proportional to input length x compiled program size. A short pattern can
compile to a large program. So the bounds are on compiled instructions, not
source length:

- at most 256 instructions per glob or pattern;
- at most 8192 summed over the taxonomy;
- at most 64 entries, 32 globs and 8 patterns per entry.

The worst case per call is therefore bounded by instructions x
`max_argument_bytes`. Measured on an Apple M1 Pro with the budget filled
with NFA-heavy patterns, that is about 1.4 s per call at 16 KiB. A
realistic taxonomy (3 entries, 7 patterns) costs about 1.3 ms. A taxonomy
over 1024 summed instructions is accepted, and startup logs a soft WARN
with its instruction count and estimated worst-case cost per call
(`1.4 s x instructions/8192 x max_argument_bytes/16 KiB`). The bound is
coarse, and character-class repeats dominate it: adding one base64-blob
detector, `[a-z0-9+/]{200,}={1,2}` (about 210 instructions, under the soft
limit, so no WARN), raises the realistic taxonomy to about 37 ms per call
at the default 16 KiB. Prefer literal-led patterns.

## Residual risk

ELIDA's semantic and temporal detection raises the cost of prompt injection;
it does not close it. Each item names what the detector cannot see and how
the failure shows up, so an operator can decide what else must stand behind
the proxy.

- **Paraphrase and distribution shift.** The classifier scores a fixed
  training distribution. A rewritten injection, an unsupported language, or
  a payload style absent from the training corpora can score below every
  threshold and leave no decision, no evidence and no correlation input.
  Nothing in telemetry distinguishes "scored clean" from "not recognized".
  The [model card](model-card-injection.md) records the corpora and the
  calibration provenance.
- **Encoded content past the decode budget.** Decoding is bounded
  (`decision.preprocessing.max_decode_depth` default 2,
  `max_representations` default 8, four base64/hex candidate runs,
  printable-text checks). A payload behind a third decode, glued to a word,
  in short or odd-length hex, or built from `\xHH` escapes of multi-byte
  UTF-8 is scored only in its encoded form. Budget exhaustion is visible as
  a `coverage_gaps` key, never as a finding.
- **Windows and coverage.** Content is scored in sentence-aligned windows of
  at most 128 tokens; inline capacity is scarce and the rest is queued.
  Meaning that exists only across windows, or only after fragments are
  joined, is not reconstructed. `coverage_complete: false` on a decision and
  the `not_assessed` and `messages_not_assessed` gaps say what was not
  scored; a request can be forwarded before any window answered.
- **Shadow and audit protect nothing.** In `shadow`, decisions never touch
  the risk score; in `audit`, evidence-only violations never do. Only
  `decision.mode: enforce`, with a matching threshold set and
  `policy.mode: enforce`, moves the ladder, and inline protection of the current request exists only on
  `inline` builds. A deployment reading shadow calibration data is
  observing, not blocking.
- **Correlation evasion.** The correlator keeps at most `max_history_events`
  (default 64) events per session and `max_sessions` (default 4096)
  sessions in per-shard LRUs. A flood of wanted events or of sessions
  evicts history (counted in `history_evicted` and `sessions_evicted`), and
  a slow burn spread wider than the budget or the rule window is not
  matched. Ending and reusing a session ID resets its history; a
  block-retained ID is inert for correlation. Fragments that all score
  below `decision.elevated_threshold` produce no events at all.
- **Retained evidence, not a complete ledger.** Ordinary correlation
  evidence is the retained display match per rule: a later firing of a rule
  in a session replaces the earlier match's evidence, and only the retained
  match reaches full persistence, a `violation_detected` row and
  session-end export. Earlier firings leave at most an event identity in
  the policy engine's in-memory risk events, which does not reconstruct
  the overwritten evidence and is not persisted. Shadow history
  keeps at most 50 entries, and carrying it across session-ID reuse does
  not recover matches already overwritten or evicted.
- **Shadow matches at session end.** A shadow match whose evaluation is
  still running when the session ends is dropped and counted in
  `shadow_matches_dropped`; the persisted `correlation_shadow` can
  therefore be short by the matches in flight at that instant.
- **Debug logging.** At logging level `debug` the proxy's pre-existing
  `response` line logs up to the first 1000 bytes of a non-streaming
  response body (tool-call arguments included), and `streaming response
  complete` logs a content preview. No semantic, correlation or evidence
  export carries content, but debug logs do.
- **Bypass.** Traffic that does not pass through the proxy is not analyzed.
  Model assets are verified at startup, not continuously.

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
- **Fragments that all stay below the elevated threshold are not
  correlated.** The slow-burn rule counts `injection_elevated` events. A
  payload split so that every fragment scores below
  `decision.elevated_threshold` produces no events. Meaning that appears
  only once the fragments are joined is not reconstructed. This is a
  residual limitation of the model, not of correlation.
- **Correlation state is in memory and bounded.** Correlation history is
  lost on restart. It is also lost when an embedder reloads the policy
  engine with a changed `correlation` block, which replaces the engine and
  is logged.
  - Each session keeps at most `max_history_events` events (default 64).
    Events older than the largest rule window are dropped.
  - At most `max_sessions` sessions (default 4096) are tracked, the least
    recently active evicted first, so a client that opens many session IDs
    can evict another session's history. Eviction is per shard, so at full
    occupancy some live sessions are evicted before the global cap is
    reached: at the default 4096, about 5% of 4096 distinct live sessions
    are evicted, and live sessions start losing history at about 85% of
    `max_sessions`. Size `max_sessions` with that headroom.
  - An evicted session also loses its cooldown and its "already matched"
    memory, so after eviction the same rule can match again on new events.
  - Eviction can cause a missed match, never a false one, and evictions are
    counted (`sessions_evicted`).
  - When no session-end consumer is enabled (storage, telemetry, OCSF,
    fingerprinting and decision all off), an ended session's state waits for
    LRU eviction.
- **One history budget per session.** `max_history_events` is shared by
  every category any rule wants. A flood of one wanted category can push
  another category's evidence out before its rule window has elapsed. With
  several categories configured, a high-volume category is therefore an
  evasion lever against a rule on a low-volume one. The shipped rule set has
  one category.
- **A reused session ID can lose or not use evidence.**
  - A session that reuses an ended session's ID within milliseconds of its
    end may lose correlation evidence observed in that interval.
  - When the previous instance was retained (blocked or terminated by the
    risk ladder), correlation is inert for the reused ID. That is harmless,
    because the ladder already refuses that ID's requests.
- **One message is identified by role and position.** `count_by: message`
  groups events by source role and message index. A client that rewrites
  its history so that indexes are reused can make two messages count as one
  (fewer matches, not more).
- **Equal timestamps keep arrival order.** Sequence steps are ordered by
  event time. Events with identical timestamps are ordered as they arrived.
- **Not every tool call is categorized.** Only tool calls in backend
  responses are categorized, and only their first `max_argument_bytes`
  bytes are matched against argument patterns. Tool calls the client sends
  back in request history are not categorized. Tool-rule violations
  (`tool_blocked`, `tool_argument_pattern`) carry no event category and
  cannot be correlation inputs; use `tool_categories` instead. Tool event
  IDs built from provider call IDs include a per-process counter, so they
  are not stable across restarts.
- **A pattern under the soft limit can still be slow.** See the taxonomy
  cost model above. One base64-blob pattern costs about 37 ms per call at
  the default `max_argument_bytes` without triggering the startup WARN.
- **The violation list shows the latest match per rule.** Every match is
  its own violation event, but the flagged-session violation list keeps one
  entry per rule name, and only that entry's contributing IDs are shown,
  persisted and exported. Earlier matches of the rule leave only an
  in-memory event identity and the rule's violation count.
- **Changing `decision.mode` at runtime splits a session's evidence.** In
  shadow, elevated decisions feed the shadow correlator; in `audit` and
  `enforce`, they feed the policy correlator. A session that crosses a mode
  change can have its slow burn split across the two and go unmatched. A
  decision is never counted twice.
- **Shadow correlation sees only semantic evidence.** The shadow
  correlator receives only elevated-band semantic decisions. A rule that
  also needs other categories (regex rule categories, tool categories)
  matches only in `audit` or `enforce`. A session not bound to the semantic
  runner (refused at the 65,536-binding safety limit) is not
  shadow-correlated. Its correlator state is in memory and bounded like the
  policy correlator's; the persisted `correlation_shadow` list keeps at
  most 50 entries per session ID.
