#!/usr/bin/env bash
# Verify a built model directory against scripts/models/pins.env.
#
# Checks, in order: manifest.json against MANIFEST_SHA256, model.onnx
# against MODEL_ONNX_SHA256, then every file the manifest lists against its
# recorded sha256, and finally that the directory holds nothing the
# manifest does not list. The manifest is pinned and lists every other
# shipped file, so a pass means the whole artifact is the pinned one, not
# merely self-consistent, and nothing unlisted ships alongside it. Used by build.sh (before it installs a build), the
# Dockerfile's model-builder stage, CI and the release workflow.
#
# Usage: [PYTHON=python3] scripts/models/verify.sh <model_dir>
set -euo pipefail

DIR="${1:?usage: scripts/models/verify.sh <model_dir>}"
PYTHON="${PYTHON:-python3}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=pins.env
source "$SCRIPT_DIR/pins.env"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

check() { # <file> <want> <pin name>
  local got
  got="$(sha256_of "$1")"
  if [[ "$got" != "$2" ]]; then
    echo "error: $1 sha256 is $got, but $3 is $2" >&2
    if [[ "$3" == manifest.files* ]]; then
      echo "The file differs from the manifest it ships with: the copy is tampered or" >&2
      echo "incomplete. Rebuild it with scripts/models/build.sh (make model)." >&2
    else
      echo "If the change is intended (the model card or another shipped file changed)," >&2
      echo "set $3=$got in scripts/models/pins.env, under review." >&2
    fi
    return 1
  fi
  echo "ok  $(basename "$1") matches $3"
}

# Report every mismatch, not just the first, so an intended change prints
# all the new digests in one run.
failed=0
check "$DIR/manifest.json" "$MANIFEST_SHA256" MANIFEST_SHA256 || failed=1
check "$DIR/model.onnx" "$MODEL_ONNX_SHA256" MODEL_ONNX_SHA256 || failed=1

# Every file the (now pinned) manifest lists, at its recorded digest, and
# nothing else: an unlisted file (say a second .onnx) would otherwise ship
# in a release archive as part of a "verified" artifact.
listing="$("$PYTHON" -I - "$DIR/manifest.json" "$DIR" <<'PY'
import json, os, sys
with open(sys.argv[1]) as fh:
    files = json.load(fh)["files"]
if not files:
    sys.exit("error: manifest lists no files")
extra = sorted(set(os.listdir(sys.argv[2])) - {"manifest.json"} - set(files))
if extra:
    sys.exit(f"error: {sys.argv[2]} holds files the manifest does not list: {extra}")
for name, digest in sorted(files.items()):
    if "/" in name or "\\" in name or name.startswith("."):
        sys.exit(f"error: manifest lists an unsafe path {name!r}")
    print(digest, name)
PY
)"
while read -r want name; do
  check "$DIR/$name" "$want" "manifest.files[$name]" || failed=1
done <<<"$listing"

if [[ "$failed" != 0 ]]; then
  echo "error: $DIR does not match scripts/models/pins.env" >&2
  exit 1
fi
echo "verified $DIR against scripts/models/pins.env"
