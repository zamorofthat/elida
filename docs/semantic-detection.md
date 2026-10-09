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
  included, reports `async_only`: results arrive after the request was
  forwarded and cannot protect it. `decision.require_inline: true` refuses to
  start on such builds.
- **One inference uses about 4.7 CPUs.** The embedded backend runs each
  inference over an intra-op worker pool, measured at about 4.7 CPUs of work
  per wall-clock second on an 8-core machine. Size `decision.max_concurrency`
  for that, not for one CPU per slot.
