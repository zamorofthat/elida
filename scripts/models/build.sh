#!/usr/bin/env bash
# Build the float32 model artifact ELIDA ships.
#
# Output: models/injection/ containing the float32 ONNX graph, the tokenizer,
# a config.json with id2label, upstream's classifier_config.json,
# manifest.json with a sha256 for every other file, the model card, and the
# upstream LICENSE. The directory is gitignored: ~90 MiB of float32 weights
# do not belong in the repository.
#
# Also prepares build/model-int8/, the upstream int8 graph laid out for
# Hugot, which the Go parity gate (TestModelParity_Float32MatchesInt8) scores
# against. It is never shipped.
#
# Idempotent: rerunning rebuilds everything from the pinned upstream commit
# and produces byte-identical files. Both outputs are assembled in staging
# directories and swapped in only when every step has succeeded, so a failed
# build never leaves a partial artifact behind.
#
# Needs only onnx==1.23.1 and numpy==2.5.3 (set PYTHON to the interpreter
# that has them). Parity is a Go test, because the measured parity is
# through the backend ELIDA actually ships.
#
# Usage: DEFENDER_COMMIT=<sha> [PYTHON=python3] scripts/models/build.sh
set -euo pipefail

DEFENDER_COMMIT="${DEFENDER_COMMIT:?set DEFENDER_COMMIT to the pinned 40-character upstream SHA}"
PYTHON="${PYTHON:-python3}"
SRC_DIR="${SRC_DIR:-build/model-src}"
INT8_DIR="${INT8_DIR:-build/model-int8}"
OUT_DIR="${OUT_DIR:-models/injection}"
MODEL_NAME="minilm-multihead"
MODEL_VERSION="v5-fp32"
THRESHOLD_SET="v1"
# Sequence length recorded in the packaged tokenizer_config.json. Hugot's
# pure-Go tokenizer ignores this key (it does not read tokenizer_config.json
# and never truncates or pads); ELIDA enforces the 128-token cut itself, in
# maxSequenceLength in internal/decision/embedded/hugot.go. Writing it here
# only documents, in the artifact, the inline length the model is run at, so
# the file does not advertise upstream's 512. It has no effect on cost.
INLINE_MAX_TOKENS=128
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

cd "$REPO_ROOT"

# -I: never import modules from the working directory or the environment.
py() { "$PYTHON" -I "$@"; }

if ! py -c 'import onnx, numpy' 2>/dev/null; then
  echo "error: $PYTHON cannot import onnx and numpy; see scripts/models/README.md" >&2
  exit 1
fi
for f in docs/model-card-injection.md scripts/models/dequantize.py; do
  if [[ ! -f "$f" ]]; then
    echo "error: $f is missing" >&2
    exit 1
  fi
done
# The pinned model.onnx digest (README, hugot_test.go) was produced with
# exactly these versions. Another version may serialize differently, so it
# is refused unless ALLOW_UNPINNED_PYTHON_DEPS=1 says the caller expects a
# different digest.
ONNX_VERSION="1.23.1"
NUMPY_VERSION="2.5.3"
HAVE_VERSIONS="$(py -c 'import onnx, numpy; print(onnx.__version__, numpy.__version__)')"
echo "using onnx/numpy $HAVE_VERSIONS (pinned $ONNX_VERSION $NUMPY_VERSION)"
if [[ "$HAVE_VERSIONS" != "$ONNX_VERSION $NUMPY_VERSION" && "${ALLOW_UNPINNED_PYTHON_DEPS:-0}" != "1" ]]; then
  echo "error: install onnx==$ONNX_VERSION numpy==$NUMPY_VERSION (see scripts/models/README.md)," >&2
  echo "or set ALLOW_UNPINNED_PYTHON_DEPS=1 and expect a different model.onnx digest." >&2
  exit 1
fi

OUT_STAGE=""
INT8_STAGE=""
cleanup() {
  [[ -n "$OUT_STAGE" ]] && rm -rf "$OUT_STAGE"
  [[ -n "$INT8_STAGE" ]] && rm -rf "$INT8_STAGE"
  return 0
}
trap cleanup EXIT

echo "== 1/4 fetch upstream, with checksum and calibration drift checks =="
DEFENDER_COMMIT="$DEFENDER_COMMIT" scripts/models/fetch.sh "$SRC_DIR"

mkdir -p "$(dirname "$OUT_DIR")" "$(dirname "$INT8_DIR")"
OUT_STAGE="$(mktemp -d "${OUT_DIR%/}.build.XXXXXX")"
INT8_STAGE="$(mktemp -d "${INT8_DIR%/}.build.XXXXXX")"

echo "== 2/4 dequantize to float32 =="
# The script takes <in_model.onnx> <out_dir> and copies tokenizer.json,
# tokenizer_config.json and config.json across itself.
py scripts/models/dequantize.py "$SRC_DIR/model_quantized.onnx" "$OUT_STAGE"

echo "== 3/4 assemble the model directory =="
cp "$SRC_DIR/LICENSE" "$OUT_STAGE/LICENSE"
cp "$SRC_DIR/classifier_config.json" "$OUT_STAGE/classifier_config.json"
cp docs/model-card-injection.md "$OUT_STAGE/MODEL_CARD.md"

# Patch config.json rather than replacing it. Upstream's config.json carries
# the BERT hyperparameters the pipeline needs (hidden_size 384,
# num_hidden_layers 6, vocab_size 30522, and the rest) but declares
# architectures: ["BertModel"] and has NO id2label, so Hugot refuses to build
# a text-classification pipeline from it. Merging preserves the
# hyperparameters; replacing the file would lose them.
#
# Head 0 is the injection detector and head 1 is the human_directed veto.
# That order IS the contract: the manifest's head_order says so,
# embedded.validateHeadLabels checks id2label against it at load, and
# embedded.normalizeHeadLabel maps the "AUX" label onto human_directed.
patch_config() {
  py - "$1" <<'PY'
import json, sys
path = sys.argv[1]
with open(path) as fh:
    cfg = json.load(fh)
before = (cfg.get("architectures"), cfg.get("id2label"))
cfg["architectures"] = ["BertForSequenceClassification"]
cfg["id2label"] = {"0": "INJECTION", "1": "AUX"}
cfg["label2id"] = {"INJECTION": 0, "AUX": 1}
with open(path, "w") as fh:
    json.dump(cfg, fh, indent=2, sort_keys=True)
    fh.write("\n")
print(f"patched {path}: architectures/id2label {before} -> "
      f"({cfg['architectures']}, {cfg['id2label']})")
PY
}
patch_config "$OUT_STAGE/config.json"

# The Go parity gate loads the int8 model through Hugot too, so it needs the
# same patch, the tokenizer, and the graph named model.onnx. It gets its own
# directory: Hugot refuses a directory holding more than one .onnx file, and
# the fetched source directory stays exactly as verified.
cp "$SRC_DIR/model_quantized.onnx" "$INT8_STAGE/model.onnx"
cp "$SRC_DIR/tokenizer.json" "$SRC_DIR/tokenizer_config.json" "$SRC_DIR/config.json" "$INT8_STAGE/"
patch_config "$INT8_STAGE/config.json"

# Record the inline sequence length in the packaged tokenizer_config.json.
# See INLINE_MAX_TOKENS above: Hugot ignores this file and ELIDA enforces the
# cut in code, so this is documentation that travels with the artifact, read
# by TestModelParity_PackagedTokenizerLengthIs128 and by humans.
py - "$OUT_STAGE/tokenizer_config.json" "$INLINE_MAX_TOKENS" <<'PY'
import json, sys
path, max_len = sys.argv[1], int(sys.argv[2])
with open(path) as fh:
    cfg = json.load(fh)
before = (cfg.get("max_length"), cfg.get("model_max_length"))
cfg["max_length"] = max_len
cfg["model_max_length"] = max_len
cfg["truncation_strategy"] = "longest_first"
cfg["padding_side"] = "right"
cfg["truncation_side"] = "right"
with open(path, "w") as fh:
    json.dump(cfg, fh, indent=2, sort_keys=True)
    fh.write("\n")
print(f"tokenizer_config.json (max_length, model_max_length) {before} -> ({max_len}, {max_len})")
PY

echo "== 4/4 write manifest =="
# Calibration comes from upstream, never from numbers invented here. Note
# the nesting: the calibration values live under "calibration", and
# optimal_threshold sits outside it.
#
# The MULTI-HEAD main threshold is not in classifier_config.json: upstream
# documents the pair on MultiheadConfig in
# src/classifiers/tier2-classifier.ts, which states that for the bundled
# default model, FP-benchmark validation gives
# { mainThreshold: 0.5, auxThreshold: 0.64 }. Those thresholds are compared
# against classifyPair() output, which is sigmoid(logit / T), so they are
# calibrated probabilities on the same scale ELIDA produces.
#
# ELIDA adopts the multi-head veto rule (injection >= main AND
# human_directed < aux), so 0.5 is the threshold in force. Do NOT substitute
# optimal_threshold: 0.4 is the operating point for the single-head
# configuration, which ELIDA does not use. The two are close enough that the
# substitution would not look broken (calibrating 0.4 as a raw probability
# gives sigmoid(ln(0.4/0.6)/2.41) = 0.458), so it would silently lower the
# detection threshold by about 0.04 rather than failing. It is recorded as
# single_head_threshold, read by nothing.
#
# Because 0.5 comes from a doc comment rather than a data file,
# TestModelParity_ManifestCalibrationMatchesFixture asserts it as a literal,
# and asserts the other values against
# internal/decision/embedded/testdata/defender-calibration-v5.json.
MAIN_THRESHOLD=0.5

py - "$OUT_STAGE" "$SRC_DIR/classifier_config.json" "$DEFENDER_COMMIT" "$MODEL_NAME" \
  "$MODEL_VERSION" "$THRESHOLD_SET" "$MAIN_THRESHOLD" <<'PY'
import hashlib, json, os, sys

out_dir, cls_path, commit, name, version, tset, main_t = sys.argv[1:8]

with open(cls_path) as fh:
    cls = json.load(fh)
cal = cls["calibration"]  # KeyError here is a loud failure, by design
temperature = float(cal["temperatureT"])
aux_t = float(cal["highRiskThreshold"])
ece = float(cal["ece"])
fitted = str(cal["fitted_on"])
single_t = float(cls["optimal_threshold"])
main_t = float(main_t)
if main_t == single_t:
    sys.exit("error: main_threshold equals optimal_threshold; see the comment above MAIN_THRESHOLD")
print(f"   temperature={temperature} main_threshold={main_t} (multi-head) "
      f"aux_threshold={aux_t} single_head_threshold={single_t} ece={ece}")

# Every shipped file except the manifest itself is checksummed, so
# embedded.Load verifies the whole artifact, provenance files included.
shipped = ("model.onnx", "tokenizer.json", "tokenizer_config.json", "config.json",
           "classifier_config.json", "LICENSE", "MODEL_CARD.md")
present = sorted(os.listdir(out_dir))
unexpected = sorted(set(present) - set(shipped))
if unexpected:
    sys.exit(f"error: unexpected files in the artifact: {unexpected}")
files = {}
for fn in shipped:
    with open(os.path.join(out_dir, fn), "rb") as fh:
        files[fn] = hashlib.sha256(fh.read()).hexdigest()

manifest = {
    "name": name,
    "version": version,
    "head_order": ["injection", "human_directed"],
    "signals": ["injection", "human_directed"],
    "calibration": {
        "temperature": temperature,
        "main_threshold": main_t,
        "aux_threshold": aux_t,
        "single_head_threshold": single_t,
        "ece": ece,
        "threshold_set": tset,
        "fitted_on": fitted,
    },
    "files": files,
    "license": "Apache-2.0",
    "source": "https://github.com/StackOneHQ/defender",
    "source_commit": commit,
    "conversion": "scripts/models/dequantize.py",
}
with open(os.path.join(out_dir, "manifest.json"), "w") as fh:
    json.dump(manifest, fh, indent=2, sort_keys=True)
    fh.write("\n")
print("wrote manifest.json")
PY

# Swap the staged directories in only now that every step has succeeded.
chmod 755 "$OUT_STAGE" "$INT8_STAGE"
rm -rf "$OUT_DIR" "$INT8_DIR"
mv "$OUT_STAGE" "$OUT_DIR"
OUT_STAGE=""
mv "$INT8_STAGE" "$INT8_DIR"
INT8_STAGE=""

echo
echo "built $OUT_DIR"
ls -l "$OUT_DIR"
echo
echo "Parity is a Go test. Run it with the int8 parity directory on disk:"
echo "  ELIDA_TEST_MODEL_PATH=$REPO_ROOT/$OUT_DIR \\"
echo "  ELIDA_TEST_INT8_MODEL_PATH=$REPO_ROOT/$INT8_DIR \\"
echo "    go test ./test/unit/ -run TestModelParity -v"
