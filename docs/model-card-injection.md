# Model card: injection detection (minilm-multihead v5-fp32)

## Identity

| Field | Value |
|---|---|
| Name | `minilm-multihead` |
| Version | `v5-fp32` |
| Architecture | BERT-style sentence encoder (all-MiniLM-L6-v2, 22M parameters) with two independent classification heads |
| Heads, in order | `0: injection` (detector), `1: human_directed` (veto) |
| Format | ONNX, float32 |
| Threshold set | `v1` |
| Sequence length | 128 tokens, special tokens included. Enforced by ELIDA in code (`maxSequenceLength` in `internal/decision/embedded/hugot.go`), which truncates on a token boundary and never pads. The `max_length` and `model_max_length` of 128 that `scripts/models/build.sh` writes into `tokenizer_config.json` only record this; the pure-Go tokenizer does not read that file. |

Record the manifest checksum from `models/injection/manifest.json` here when the artifact is promoted. The checksum, not the version string, is what identifies a specific build.

## Provenance

Derived from the StackOne Defender project (https://github.com/StackOneHQ/defender), file `src/classifiers/models/minilm-multihead-v5/model_quantized.onnx`, at a pinned commit recorded in `manifest.json` as `source_commit` (`ff83e70981099f9261520e0e9bc6cb85bb9789da`). `scripts/models/fetch.sh` verifies every downloaded file against a pinned SHA-256.

Upstream publishes only a dynamically quantized int8 ONNX graph. No float32 or safetensors weights are published. The float32 graph ELIDA ships is **derived by dequantization**: `scripts/models/dequantize.py` rewrites each `DynamicQuantizeLinear` → `MatMulInteger` → `Cast` → `Mul` subgraph into a float32 `MatMul` whose weight initializer is `(W_int8 - zero_point) * scale`, rewrites each quantized embedding table read through `Gather` → `DequantizeLinear` into a plain `Gather` over a float32 table, and drops the runtime activation quantization. Dequantizing the stored weights is exact, so the result is at least as faithful to the original fine-tune as the int8 file is.

The conversion rewrites 38 `MatMulInteger` chains and 3 quantized embedding tables, taking the graph from 748 to 606 nodes and the file from 23.0 MB to 90.4 MB. `onnx.checker.check_model(full_check=True)` passes and no quantized operator or int8 initializer survives.

Conversion is verified, not assumed. `TestModelParity_Float32MatchesInt8` scores a fixed ten-probe set (`scripts/models/probes.json`) through both models in the pure-Go backend ELIDA actually ships, and fails if any calibrated probability differs by more than 1e-4 or if any decision flips under the packaged thresholds. The spike measured **1.1e-6 on the main head and 1.0e-6 on the aux head**. Parity is this close because the Go backend emulates int8 operators in float rather than running real int8 kernels, so the two graphs are doing nearly the same arithmetic.

The int8 graph is **not shipped**. Measured on an arm64 Mac: float32 runs at **184 ms p50 / 232 ms p99** against int8's **618 ms / 1050 ms**, a 3.4x gap, with a 127 ms model load and about 405 MiB of heap after warmup.

## License

Apache-2.0, carried verbatim in `models/injection/LICENSE` from the upstream repository at the pinned commit. The converted artifact is a derivative work of StackOne Defender's `minilm-multihead-v5` model: ELIDA modified it by dequantizing the int8 graph to float32, adding `id2label`, `label2id` and a sequence-classification architecture to `config.json`, and recording a 128-token sequence length in `tokenizer_config.json`. This card is the notice of those changes. Redistribution rights for the converted artifact were reviewed before release; `embedded.Load` refuses a manifest with no `license` field.

## Calibration

Values are read from upstream's `classifier_config.json` at build time, never typed into a script:

| Field | Source | Value |
|---|---|---|
| Temperature | `classifier_config.json` → `calibration.temperatureT` | `2.41` |
| Main (`injection`) threshold | `tier2-classifier.ts` `MultiheadConfig` | `0.5` |
| Auxiliary (`human_directed`) veto threshold | `classifier_config.json` → `calibration.highRiskThreshold` | `0.64` |
| Single-head operating point, reference only | `classifier_config.json` → `optimal_threshold` | `0.4` |
| Expected calibration error | `classifier_config.json` → `calibration.ece` | `0.09` |
| Fitted on | `classifier_config.json` → `calibration.fitted_on` | labeled plugin events, 2026-05-13 |

The decision rule in force is `injection >= 0.5 AND human_directed < 0.64` on **calibrated** probabilities.

**Why the main threshold is 0.5 and not the 0.4 in upstream's config file.** This model has two heads and ELIDA uses both: the auxiliary head vetoes a directive aimed at a human reader. Upstream documents the threshold pair for that configuration on `MultiheadConfig` in `src/classifiers/tier2-classifier.ts`: the Tier 2 classifier reads the output as `[main, aux]` and blocks iff `main >= mainThreshold AND aux < auxThreshold`, and for the bundled default model, FP-benchmark validation gives `{ mainThreshold: 0.5, auxThreshold: 0.64 }`. Those are compared against `classifyPair()` output, which is `sigmoid(logit / T)`, so they are calibrated probabilities on the same scale this provider produces.

The `optimal_threshold: 0.4` in `classifier_config.json` sits outside the `calibration` object and is the operating point for the *single-head* configuration, where no veto is applied and a lower cut compensates. The two describe nearly the same decision on different scales: calibrating 0.4 as a raw probability gives `sigmoid(ln(0.4/0.6) / 2.41) = 0.458`. That closeness is the hazard, not a reassurance — a build that used 0.4 would lower the detection threshold by about 0.04 on the calibrated scale and produce scores that look entirely reasonable. Both values are recorded in the manifest, as `main_threshold` and `single_head_threshold`, and only 0.5 is read.

Upstream's own note records that a raw `highRiskThreshold` of 0.8 is math-equivalent to a calibrated 0.64 at temperature 2.41, which is why 0.64 is compared post-temperature and not against a raw logit.

These values are pinned twice, against two different failure modes. `scripts/models/testdata/classifier_config.json` holds the whole upstream file, and `scripts/models/fetch.sh` diffs the download against it, so an upstream change to a threshold or the dataset list fails the build rather than being absorbed by a rebuild. `internal/decision/embedded/testdata/defender-calibration-v5.json` holds just the calibration block, and `TestModelParity_ManifestCalibrationMatchesFixture` asserts the generated `manifest.json` equals it, so a build script reading the wrong JSON path fails CI rather than shipping a wrong temperature.

The packaged pipeline applies a sigmoid **without** this temperature. ELIDA therefore computes `p = sigmoid(logit / 2.41)` itself, in `embedded.calibrate`. Skipping it would make every score overconfident: `sigmoid(6.0)` is 0.9975 where `sigmoid(6.0/2.41)` is 0.9234.

Probabilities support ranking and threshold selection. They are **not** literal risk percentages, and an expected calibration error of 0.09 is a reminder of that.

## Intended use

Scoring request-side user content and untrusted tool results for prompt-injection intent, inside ELIDA, as evidence feeding the existing session risk score. It is one signal among several, never the sole basis for an enforcement action.

## Not intended for

- Response-side compliance classification. The `compliance` signal is reserved in the contract and no packaged head answers it.
- Any use outside a bounded window. Feeding a 1,268-character benign text as a single input scored 0.905, so content is split into sentence-aligned windows of at most 128 tokens.
- **Languages other than English.** Upstream's dataset list includes `multilingual-hardneg`, which is a multilingual *negative* set rather than multilingual coverage, so non-English injection is not something this model is evaluated for and ELIDA has not evaluated it either.

## Limitations

- **Adversarial paraphrase.** A rewritten injection that avoids the training distribution can score low. This model raises the cost of injection; it does not close it.
- **Distribution shift.** Calibration was fitted on upstream's plugin events, not on ELIDA traffic. Thresholds must be recalibrated on representative traffic before enforcement.
- **The auxiliary head is a veto, not a detector.** A high `human_directed` rescues content the main head would flag. It can therefore also rescue a real injection that is phrased as documentation or a runbook.
- **Partial coverage.** Inline capacity is scarce. A long message may have only one window scored before forwarding, with the rest arriving asynchronously. Coverage is reported on every decision and is never represented as a clean full scan.
- **Architecture.** GoMLX's accelerated kernels are gated to `amd64 && goexperiment.simd`. Other architectures run a scalar path roughly 20x slower and are async-only.
- **Encoded content.** Decoding is bounded (depth, representation count, byte budget) and deliberately conservative. A payload behind three decodes, or one whose decoded form fails the printable-text check, is not analyzed.
- **The 128-token cut lives in code, not in the artifact.** The pure-Go backend does not truncate or pad, and it runs the graph at exactly the input's token count, so cost scales with the real length of each window. ELIDA's `maxSequenceLength` is what keeps every inference at or under 128 tokens; the tokenizer configuration in the artifact does not. A different runtime that honors `tokenizer_config.json` would see the same 128, but ELIDA does not rely on that.

## Evaluation

Upstream reports an expected calibration error of 0.09, fitted on labeled plugin events from 2026-05-13, over 23 named public and internal corpora: `qualifire`, `jayavibhav`, `agentdojo`, `jasperls`, `jailbreakbench`, `toxic-chat`, `chatgpt-jailbreaks`, `email-hardneg`, `email-hardneg-gen`, `multilingual-hardneg`, `jailbreakbench-neg`, `toxic-chat-neg`, `fujitsu-injecagent`, `fujitsu-rag`, `enron-ham`, `connector-hardneg-v2`, `dev-tooling-hardneg-curated`, `dev-tooling-attacks`, `agentshield-shape-attacks`, `system-prompt-extraction-attacks`, `emoji-ci-benign`, `benign-user-queries`, `code-docs-benign`. That list is also the corpus set the fallback-training path would use.

ELIDA has not independently evaluated precision, recall or false-positive rate on ELIDA traffic. Until it has, `decision.mode` stays at `shadow` or `audit`: the release gates require recorded calibration evidence for the exact model and threshold-set versions before enforcement.

## Supply chain

- `models/injection/` is produced only by `scripts/models/build.sh` from the pinned upstream commit, with every upstream file checked against a pinned SHA-256.
- `manifest.json` records a SHA-256 for every other file in the directory; `embedded.Load` verifies all of them before the provider reports healthy.
- ELIDA never downloads or replaces a model at runtime. Upgrades and rollbacks are explicit operator actions.
- Production model directories should be mounted read-only.
