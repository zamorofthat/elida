# Semantic injection detection: behaviour, configuration and limits

> The limitations list below will be folded into the root
> `SECURITY_LIMITATIONS.md` once PR #181 merges.

Semantic prompt-injection detection (`decision.*`) runs a packaged MiniLM
classifier over request content and its decoded representations. Its
behaviour and configuration rules (`require_inline`, the `max_concurrency`
split, when enforcement is refused, allowlisted tools, retained sessions) are
in [Configuration](configuration.md#semantic-injection-detection-decision).

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
