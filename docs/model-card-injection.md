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

The conversion is verified, within a stated scope. `TestModelParity_Float32MatchesInt8` scores a fixed ten-probe set (`scripts/models/probes.json`) through both graphs in the pure-Go backend ELIDA actually ships, and fails if any calibrated probability differs by more than 1e-4 or if any decision flips under the packaged thresholds. Measured: **1.05e-6 on the main head and 1.00e-6 on the aux head**. Parity is this close because the Go backend never runs int8 arithmetic: it fuses each int8 matmul chain into a float-activation matmul over dequantized weights, so both sides compute nearly the same function. The gate therefore proves that `dequantize.py` reproduces the backend's own dequantization. It does **not** show that the float32 model scores like upstream's deployment, which runs the int8 graph with real onnxruntime int8 kernels; that difference is measured separately under [Calibration provenance](#calibration-provenance).

The int8 graph is **not shipped**. Measured on an arm64 Mac: float32 runs at **184 ms p50 / 232 ms p99** against int8's **618 ms / 1050 ms**, a 3.4x gap, with a 127 ms model load and about 405 MiB of heap after warmup.

## License

Apache-2.0, carried verbatim in `models/injection/LICENSE` from the upstream repository at the pinned commit. The converted artifact is a derivative work of StackOne Defender's `minilm-multihead-v5` model: ELIDA modified it by dequantizing the int8 graph to float32, adding `id2label`, `label2id` and a sequence-classification architecture to `config.json`, and recording a 128-token sequence length in `tokenizer_config.json`. This card is the notice of those changes. Apache-2.0 permits redistributing derivative works provided the license accompanies them, modified files carry notice of the changes, and upstream NOTICE content is retained; upstream ships no NOTICE file at the pinned commit. `embedded.Load` refuses a manifest with no `license` field.

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

## Calibration provenance

Upstream's thresholds (`main 0.5`, `aux 0.64`) and temperature (`T = 2.41`, ECE 0.09) were fitted and validated on scores from the **int8** graph running under **onnxruntime** with real int8 kernels, where activations are quantized at run time. ELIDA scores the **float32** graph in Hugot's pure-Go backend, where nothing is quantized. The two paths do not produce identical scores, so upstream's calibration transfers to ELIDA only approximately.

Measured on the parity probe set, with calibrated probabilities from the shipped `models/injection/model.onnx` and upstream's int8 graph, both under onnxruntime 1.30.0 (CPU), tokenizers 0.23.2 truncating at 128 tokens, and the manifest's temperature and thresholds:

```bash
python -I scripts/models/ort_reference.py --compare-int8 \
  models/injection build/model-int8 scripts/models/probes.json
```

| probe | main fp32 | main ort-int8 | Δ main | aux fp32 | aux ort-int8 | Δ aux | flag fp32 / int8 |
|---|---|---|---|---|---|---|---|
| `benign-short` | 0.0715 | 0.0652 | 6.2e-03 | 0.8356 | 0.8191 | 1.6e-02 | False / False |
| `benign-short2` | 0.0386 | 0.0371 | 1.5e-03 | 0.7419 | 0.7263 | 1.6e-02 | False / False |
| `benign-tool` | 0.7868 | 0.8507 | 6.4e-02 | 0.5266 | 0.4764 | 5.0e-02 | True / True |
| `inj-ignore` | 0.9684 | 0.9681 | 3.2e-04 | 0.1100 | 0.1092 | 8.7e-04 | True / True |
| `inj-dan` | 0.8587 | 0.8711 | 1.2e-02 | 0.0559 | 0.0574 | 1.5e-03 | True / True |
| `inj-exfil` | 0.9632 | 0.9669 | 3.8e-03 | 0.2424 | 0.2186 | 2.4e-02 | True / True |
| `inj-override` | 0.9677 | 0.9716 | 3.9e-03 | 0.1635 | 0.1831 | 2.0e-02 | True / True |
| `inj-creds` | 0.9767 | 0.9771 | 3.7e-04 | 0.1791 | 0.1854 | 6.3e-03 | True / True |
| `code-snippet` | 0.0783 | 0.0762 | 2.2e-03 | 0.8515 | 0.8528 | 1.3e-03 | False / False |
| `human-directed` | 0.6782 | 0.6683 | 9.9e-03 | 0.0756 | 0.0757 | 1.2e-04 | True / True |

Worst delta: **main 6.4e-2, aux 5.0e-2**; no decision flipped on these ten probes. (The float32 numbers are the same through Hugot to within 1.1e-6; see Provenance.)

So ELIDA's float32 path shifts calibrated probabilities by up to about 0.064 relative to the scoring upstream calibrated on. Ten probes, none of them close to a threshold, cannot bound that shift in general, and no-flip on this set is not evidence that decisions near 0.5 or 0.64 are preserved. **Recalibrating the temperature and thresholds on the float32 path is an open follow-up**; until it is done, the thresholds above are upstream's, used as they are. The release gate under [Evaluation](#evaluation) (recorded calibration evidence for the exact model and threshold-set versions before enforcement) has to be met on this float32 model, not inferred from upstream's int8 numbers.

## Intended use

Scoring request-side user content and untrusted tool results for prompt-injection intent, inside ELIDA, as evidence feeding the existing session risk score. It is one signal among several, never the sole basis for an enforcement action.

## Not intended for

- Response-side compliance classification. The `compliance` signal is reserved in the contract and no packaged head answers it.
- Any use outside a bounded window. Feeding a 1,268-character benign text as a single input scored 0.905, so content is split into sentence-aligned windows of at most 128 tokens.
- **Languages other than English.** Upstream's dataset list includes `multilingual-hardneg`, which is a multilingual *negative* set rather than multilingual coverage, so non-English injection is not something this model is evaluated for and ELIDA has not evaluated it either.

## Limitations

- **Adversarial paraphrase.** A rewritten injection that avoids the training distribution can score low. This model raises the cost of injection; it does not close it.
- **Distribution shift.** Calibration was fitted on upstream's plugin events, not on ELIDA traffic. Thresholds must be recalibrated on representative traffic before enforcement.
- **Calibration transfer.** Upstream fitted its calibration on onnxruntime int8 scores; ELIDA runs float32 in a different backend, which moved calibrated probabilities by up to about 0.064 on the probe set. See [Calibration provenance](#calibration-provenance).
- **The auxiliary head is a veto, not a detector.** A high `human_directed` rescues content the main head would flag. It can therefore also rescue a real injection that is phrased as documentation or a runbook.
- **Partial coverage.** Inline capacity is scarce. A long message may have only one window scored before forwarding, with the rest arriving asynchronously. Coverage is reported on every decision and is never represented as a clean full scan.
- **Architecture.** GoMLX's accelerated kernels are gated to `amd64 && goexperiment.simd`. Other architectures run a scalar path roughly 20x slower and are async-only.
- **Encoded content.** Decoding is bounded (depth, representation count, byte budget) and deliberately conservative. A payload behind three decodes, or one whose decoded form fails the printable-text check, is not analyzed.
- **The 128-token cut lives in code, not in the artifact.** The pure-Go backend does not truncate or pad, and it runs the graph at exactly the input's token count, so cost scales with the real length of each window. ELIDA's `maxSequenceLength` is what keeps every inference at or under 128 tokens; the tokenizer configuration in the artifact does not. A different runtime that honors `tokenizer_config.json` would see the same 128, but ELIDA does not rely on that.

## Evaluation

Upstream reports an expected calibration error of 0.09, fitted on labeled plugin events from 2026-05-13, over 23 named public and internal corpora: `qualifire`, `jayavibhav`, `agentdojo`, `jasperls`, `jailbreakbench`, `toxic-chat`, `chatgpt-jailbreaks`, `email-hardneg`, `email-hardneg-gen`, `multilingual-hardneg`, `jailbreakbench-neg`, `toxic-chat-neg`, `fujitsu-injecagent`, `fujitsu-rag`, `enron-ham`, `connector-hardneg-v2`, `dev-tooling-hardneg-curated`, `dev-tooling-attacks`, `agentshield-shape-attacks`, `system-prompt-extraction-attacks`, `emoji-ci-benign`, `benign-user-queries`, `code-docs-benign`. That list is also the corpus set the fallback-training path would use.

ELIDA has not independently evaluated precision, recall or false-positive rate on ELIDA traffic, and has not refitted upstream's calibration on the float32 path it actually runs (see [Calibration provenance](#calibration-provenance)). Until it has, `decision.mode` stays at `shadow` or `audit`: the release gates require recorded calibration evidence for the exact model and threshold-set versions before enforcement.

## Supply chain

- `models/injection/` is produced only by `scripts/models/build.sh` from the pinned upstream commit, with every upstream file checked against a pinned SHA-256.
- `manifest.json` records a SHA-256 for every other file in the directory; `embedded.Load` verifies all of them before the provider reports healthy.
- ELIDA never downloads or replaces a model at runtime. Upgrades and rollbacks are explicit operator actions.
- Production model directories should be mounted read-only.
