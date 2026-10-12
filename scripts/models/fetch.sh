#!/usr/bin/env bash
# Fetch the upstream StackOne Defender model files at a pinned commit.
#
# Upstream publishes only the dynamically quantized int8 ONNX. This script
# retrieves it verbatim; scripts/models/dequantize.py derives the float32
# graph ELIDA ships, because int8 measured 618ms p50 against float32's 184ms
# in the pure-Go backend.
#
# Every file is verified against a SHA-256 pinned below, and
# classifier_config.json is additionally diffed against the committed
# fixture. Downloads land in a staging directory that replaces OUT_DIR only
# after every check passes, so a failed fetch never leaves a partial or
# unverified source directory behind.
#
# Usage: DEFENDER_COMMIT=<sha> scripts/models/fetch.sh [out_dir]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# PINNED_DEFENDER_COMMIT comes from pins.env: the commit this script, its
# checksums and the fixtures were written against (git ls-remote
# https://github.com/StackOneHQ/defender.git HEAD on 2026-10-07).
# Reproducibility depends on it not floating: moving it means updating the
# checksums below, both calibration fixtures, the model card and the
# artifact digests in pins.env, under review.
# shellcheck source=pins.env
source "$SCRIPT_DIR/pins.env"

DEFENDER_COMMIT="${DEFENDER_COMMIT:?set DEFENDER_COMMIT to the pinned 40-character upstream SHA}"
DEFENDER_REPO="https://github.com/StackOneHQ/defender"
MODEL_SUBDIR="src/classifiers/models/minilm-multihead-v5"
OUT_DIR="${1:-build/model-src}"
FIXTURE="$SCRIPT_DIR/testdata/classifier_config.json"

if [[ "$DEFENDER_COMMIT" != "$PINNED_DEFENDER_COMMIT" ]]; then
  echo "error: DEFENDER_COMMIT=$DEFENDER_COMMIT but scripts/models/pins.env pins $PINNED_DEFENDER_COMMIT." >&2
  echo "Moving the pin is a model change: update pins.env, the checksums below," >&2
  echo "both calibration fixtures and docs/model-card-injection.md together, under review." >&2
  exit 1
fi

# SHA-256 of each file at the pinned commit. The five model files are the
# entire published model directory; LICENSE is the repository's Apache-2.0
# license, carried into the artifact.
EXPECTED_SHA256="
model_quantized.onnx 68685f34a646d66c53239d9ee54acd279803f507f70f86f9f99854e3f08368c8
tokenizer.json 0d3aef594edd5f9b53e7f814277a9171dc70ff93eb66bda6e01f7aa53997d963
tokenizer_config.json 7157371fc530fa70df2d0b4aa568da99a6f0ebca2c96405426382ea6476125b5
config.json 9ac5b401b1ad005e0e2c3f7dfe5d05bbb59a4368b59438cd24bc93d5baa3c280
classifier_config.json bf607ef8ed9a6d27fc26f4089413e8a82202bab3ff94b1a48ccff59524965fcf
LICENSE bd7e4afb04e979ba9bd17afdc88fd525489ff2489ee015e3071d0c5723079244
"

if [[ ! -f "$FIXTURE" ]]; then
  echo "error: pinned fixture $FIXTURE is missing" >&2
  exit 1
fi

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

raw_url() {
  printf '%s/raw/%s/%s' "$DEFENDER_REPO" "$DEFENDER_COMMIT" "$1"
}

mkdir -p "$(dirname "$OUT_DIR")"
STAGE="$(mktemp -d "${OUT_DIR%/}.fetch.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT

while read -r name want; do
  [[ -z "$name" ]] && continue
  if [[ "$name" == "LICENSE" ]]; then
    path="LICENSE"
  else
    path="$MODEL_SUBDIR/$name"
  fi
  echo "fetching $name"
  curl --fail --silent --show-error --location --retry 3 \
    --proto '=https' --proto-redir '=https' --max-redirs 3 \
    --output "$STAGE/$name" "$(raw_url "$path")"
  got="$(sha256_of "$STAGE/$name")"
  if [[ "$got" != "$want" ]]; then
    echo "error: $name sha256 $got does not match the pinned $want" >&2
    exit 1
  fi
done <<<"$EXPECTED_SHA256"
echo "all files match their pinned sha256"

echo "$DEFENDER_COMMIT" >"$STAGE/SOURCE_COMMIT"

# Calibration drift check. The pinned fixture is what this plan, the model
# card and the tests were written against. If upstream changed a threshold,
# the temperature or the dataset list, that is a model change requiring
# review, not something a rebuild should absorb. (The checksum above already
# implies this at the pinned commit; the diff names what changed.)
if ! diff -u "$FIXTURE" "$STAGE/classifier_config.json"; then
  echo "::error::upstream classifier_config.json differs from scripts/models/testdata/classifier_config.json" >&2
  echo "Calibration or dataset provenance changed. This is a model change and needs security review." >&2
  echo "If the change is intended: update the fixture, the model card and the thresholds, then rerun." >&2
  exit 1
fi
echo "calibration matches the pinned fixture"

# mktemp -d creates the stage owner-only; the verified source is ordinary
# build scratch, readable like any other build directory.
chmod 755 "$STAGE"
rm -rf "$OUT_DIR"
mv "$STAGE" "$OUT_DIR"
trap - EXIT

echo "fetched upstream model files at $DEFENDER_COMMIT into $OUT_DIR"
ls -l "$OUT_DIR"
