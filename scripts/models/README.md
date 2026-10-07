# Model build scripts

`models/injection/` is generated, gitignored, and roughly 90 MiB. Build it:

```bash
python3 -m venv build/model-venv
build/model-venv/bin/pip install 'onnx>=1.17' numpy
PYTHON=build/model-venv/bin/python3 \
  DEFENDER_COMMIT=ff83e70981099f9261520e0e9bc6cb85bb9789da \
  scripts/models/build.sh
```

Only `onnx` and `numpy` are needed. Parity is a Go test, not a Python one, so the build has no `onnxruntime` or `tokenizers` dependency. The reference build used onnx 1.23.1 and numpy 2.5.3; the resulting `model.onnx` SHA-256 is `13febddd90418e64b9285543f31a92534f778cb43ca728b33e46b2dba8848f6a`, which `internal/decision/embedded/hugot_test.go` also pins.

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
| `ort_reference.py` | Developer tool: regenerates the onnxruntime reference logits pinned in `internal/decision/embedded/hugot_test.go`. Needs `onnxruntime` and `tokenizers`; not part of the build and not a gate. |
| `testdata/classifier_config.json` | The whole upstream file at the pinned commit, byte for byte. Drift here is a model change needing review. |

The calibration block is pinned a second time at `internal/decision/embedded/testdata/defender-calibration-v5.json`, where `TestModelParity_ManifestCalibrationMatchesFixture` checks the generated manifest against it. The two guard different things: this one catches upstream changing, that one catches `build.sh` reading the wrong JSON path.

`tokenizer_config.json` is patched to `max_length` and `model_max_length` 128 as documentation only. Hugot's pure-Go tokenizer does not read that file and never truncates or pads; ELIDA enforces the 128-token cut in code (`maxSequenceLength` in `internal/decision/embedded/hugot.go`).

Parity lives in `test/unit/model_parity_test.go` and runs through Hugot, the backend ELIDA ships:

```bash
ELIDA_TEST_MODEL_PATH="$PWD/models/injection" \
ELIDA_TEST_INT8_MODEL_PATH="$PWD/build/model-int8" \
  go test ./test/unit/ -run TestModelParity -v
```

Under the race detector, add `-gcflags=all=-d=checkptr=0`: GoMLX aborts under checkptr, so the real-model tests skip under plain `-race`.

`DEFENDER_COMMIT` is required and has no default, and `fetch.sh` refuses any value other than the commit its checksums were recorded at. A floating upstream reference would make the artifact unreproducible and the manifest checksum meaningless. Moving the pin means updating `fetch.sh`'s checksums, both calibration fixtures and `docs/model-card-injection.md` together, under review.

Never ship the int8 graph: it measured 618 ms p50 against float32's 184 ms in the pure-Go backend.
