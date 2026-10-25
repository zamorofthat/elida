# Model build scripts

`models/injection/` is generated, gitignored, and roughly 90 MiB. Build it with `make model`, which creates `build/model-venv` with the pinned Python packages and does nothing when the artifact is already newer than `scripts/models/` and the model card. `make test-model` then runs the model-backed tests. By hand:

```bash
source scripts/models/pins.env
python3 -m venv build/model-venv
build/model-venv/bin/pip install 'onnx==1.23.1' 'numpy==2.5.3'
PYTHON=build/model-venv/bin/python3 \
  DEFENDER_COMMIT="$PINNED_DEFENDER_COMMIT" \
  scripts/models/build.sh
```

Only `onnx` and `numpy` are needed, at exactly onnx 1.23.1 and numpy 2.5.3: `build.sh` refuses other versions unless `ALLOW_UNPINNED_PYTHON_DEPS=1`, because another version may serialize a different graph. Parity is a Go test, not a Python one, so the build has no `onnxruntime` or `tokenizers` dependency.

## Pins

`pins.env` is the single source of the model pins. `fetch.sh`, `build.sh`, `verify.sh`, the Makefile, `scripts/docker-push.sh`, the Dockerfile, the CI and release workflows, `internal/decision/embedded/hugot_test.go` and `test/unit/model_pins_test.go` all read it. `docker-compose.yaml` is the exception, because Compose cannot read a file for a build-arg default. It carries the commit as a literal, and `TestModelPins_ComposeDefaultMatches` keeps that literal in sync.

| Key | Pins |
|---|---|
| `PINNED_DEFENDER_COMMIT` | The upstream commit. `fetch.sh` refuses any other `DEFENDER_COMMIT`. |
| `MODEL_ONNX_SHA256` | The float32 `model.onnx` that commit and the pinned Python versions produce. |
| `MANIFEST_SHA256` | The built `manifest.json`. The manifest records a SHA-256 for every other shipped file, so this pins the whole artifact. |

`verify.sh <dir>` checks a model directory against all three: `manifest.json`, `model.onnx`, then every file the manifest lists. `build.sh` runs it before it installs a build, and refuses to replace `models/injection/` on a mismatch. The Dockerfile's model-builder stage, the CI Model Artifact job (on a cache hit as well as a fresh build) and the release job run it too.

**Any change to the model card or to any other shipped file changes `manifest.json`, so it requires bumping `MANIFEST_SHA256`.** That includes `docs/model-card-injection.md`, which ships as `MODEL_CARD.md`. On a mismatch `build.sh` prints the new digest. Set it in `pins.env` under review and rerun. A change to the graph also moves `MODEL_ONNX_SHA256`; moving `PINNED_DEFENDER_COMMIT` moves everything (see the end of this file).

The build writes two directories, both gitignored:

- `models/injection/`: the shipped artifact (`model.onnx`, `tokenizer.json`, `tokenizer_config.json`, `config.json`, `classifier_config.json`, `MODEL_CARD.md`, `LICENSE`, `manifest.json`).
- `build/model-int8/`: upstream's int8 graph laid out for Hugot, used only by the parity gate. Never ship it.

`build/model-src/` holds the verified upstream download. Each output is assembled in a staging directory and swapped in only when every step succeeds. Rerunning produces byte-identical files.

Then point ELIDA at it:

```bash
ELIDA_DECISION_ENABLED=true ELIDA_DECISION_MODEL_PATH="$PWD/models/injection" ./elida
```

or run the model-backed tests:

```bash
ELIDA_TEST_MODEL_PATH="$PWD/models/injection" go test ./test/unit/ -run TestRealModel -v
```

| Script | Purpose |
|---|---|
| `fetch.sh` | Download upstream files at `DEFENDER_COMMIT`, verify each against a pinned SHA-256, and fail if `classifier_config.json` has drifted from `testdata/`. |
| `dequantize.py` | Rewrite the int8 graph to float32: 38 matmul chains and 3 embedding tables. Adopted from the validated spike; `onnx` only. |
| `build.sh` | Run both, patch `config.json` and `tokenizer_config.json`, write `manifest.json`, prepare `build/model-int8/`. Idempotent. |
| `probes.json` | The ten fixed inputs the parity gate scores. |
| `ort_reference.py` | Developer tool, not part of the build and not a gate; needs `onnxruntime` 1.30.0 and `tokenizers` 0.23.2. `<dir>` regenerates the reference logits pinned in `internal/decision/embedded/hugot_test.go`; `--compare-int8 models/injection build/model-int8 scripts/models/probes.json` prints the float32 vs real-onnxruntime-int8 delta table recorded in the model card. |
| `testdata/classifier_config.json` | The whole upstream file at the pinned commit, byte for byte. Drift here is a model change needing review. |

The calibration block is pinned a second time at `internal/decision/embedded/testdata/defender-calibration-v5.json`, where `TestModelParity_ManifestCalibrationMatchesFixture` checks the generated manifest against it. The two guard different things: this one catches upstream changing, that one catches `build.sh` reading the wrong JSON path.

`tokenizer_config.json` is patched to `max_length` and `model_max_length` 128 as documentation only. Hugot's pure-Go tokenizer does not read that file and never truncates or pads; ELIDA enforces the 128-token cut in code (`maxSequenceLength` in `internal/decision/embedded/hugot.go`).

Parity lives in `test/unit/model_parity_test.go` and runs through Hugot, the backend ELIDA ships. It proves `dequantize.py` matches the backend's own dequantization of the int8 graph; it does not compare against upstream's onnxruntime int8 scoring, which `ort_reference.py --compare-int8` measures (see the model card's Calibration provenance):

```bash
ELIDA_TEST_MODEL_PATH="$PWD/models/injection" \
ELIDA_TEST_INT8_MODEL_PATH="$PWD/build/model-int8" \
  go test ./test/unit/ -run TestModelParity -v
```

Under the race detector, add `-gcflags=all=-d=checkptr=0`: GoMLX aborts under checkptr, so the real-model tests skip under plain `-race`.

`build.sh` and `fetch.sh` require `DEFENDER_COMMIT` and give it no default (`make model` passes `PINNED_DEFENDER_COMMIT`). `fetch.sh` refuses any value other than `PINNED_DEFENDER_COMMIT` in `pins.env`, the commit its checksums were recorded at. A floating upstream reference would make the artifact unreproducible and the manifest checksum meaningless. Moving the pin is one reviewed change that updates all of these together:

- `PINNED_DEFENDER_COMMIT` in `pins.env`
- `fetch.sh`'s checksums
- both calibration fixtures
- `docs/model-card-injection.md`
- `MODEL_ONNX_SHA256` and `MANIFEST_SHA256` in `pins.env`
- the literal default in `docker-compose.yaml`

Never ship the int8 graph: it measured 618 ms p50 against float32's 184 ms in the pure-Go backend.
