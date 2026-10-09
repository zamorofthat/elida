package unit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"elida/internal/decision"
	"elida/internal/decision/embedded"
)

// parityTolerance is the maximum allowed difference in calibrated
// probability between the shipped float32 model and the int8 source, both
// scored through Hugot's pure-Go backend.
//
// Measured: 1.05e-6 on the main head and 1.00e-6 on the aux head. It is
// near-exact because the backend never runs int8 arithmetic: onnx-gomlx
// fuses each DynamicQuantizeLinear -> MatMulInteger -> Cast -> Mul chain
// into a float-activation matmul over dequantized weights, so both sides
// compute nearly the same function. 1e-4 leaves two orders of magnitude of
// headroom and still catches a mis-dequantized weight.
const parityTolerance = 1e-4

// int8ModelPathEnv points at the int8 parity directory build.sh prepares
// (build/model-int8: the upstream int8 graph as model.onnx, with the same
// config.json patch as the shipped model).
const int8ModelPathEnv = "ELIDA_TEST_INT8_MODEL_PATH"

// pinnedInt8ModelSHA256 is the SHA-256 of upstream's model_quantized.onnx at
// the commit scripts/models/fetch.sh pins. The int8 directory has no
// manifest, so the gate checks this itself: a stale or hand-edited
// build/model-int8 must fail, not be compared silently.
const pinnedInt8ModelSHA256 = "68685f34a646d66c53239d9ee54acd279803f507f70f86f9f99854e3f08368c8"

type parityProbe struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func loadProbes(t *testing.T) []parityProbe {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "models", "probes.json"))
	if err != nil {
		t.Fatalf("read probes.json: %v", err)
	}
	var probes []parityProbe
	if err := json.Unmarshal(raw, &probes); err != nil {
		t.Fatalf("parse probes.json: %v", err)
	}
	if len(probes) == 0 {
		t.Fatal("probes.json is empty")
	}
	return probes
}

// TestModelParity_Float32MatchesInt8 is the conversion gate.
//
// What it compares: the float32 graph scripts/models/dequantize.py derives,
// scored through Provider.Decide, against upstream's int8 graph scored
// through the same Hugot backend and calibrated with the same temperature,
// on the fixed probes in scripts/models/probes.json.
//
// What that shows: the offline dequantization is equivalent to the
// backend's own handling of the int8 graph, which dequantizes weights and
// keeps activations in float. A wrong scale, zero point, axis or rewired
// node fails it.
//
// What it does NOT show: that the float32 model scores like upstream's
// deployment. Upstream runs the int8 graph under onnxruntime with real int8
// kernels (activations quantized too), and its thresholds and temperature
// were fitted on those scores. That gap is measured separately, not gated:
// up to about 0.064 calibrated probability on these probes with no decision
// flips (scripts/models/ort_reference.py --compare-int8; recorded in
// docs/model-card-injection.md under "Calibration provenance").
func TestModelParity_Float32MatchesInt8(t *testing.T) {
	if _, ok := embedded.TestModelPath(); !ok {
		t.Skipf("set %s to the built float32 model directory", embedded.TestModelPathEnv)
	}
	int8Dir := os.Getenv(int8ModelPathEnv)
	if int8Dir == "" {
		t.Skipf("set %s to the int8 parity directory build.sh prepares", int8ModelPathEnv)
	}
	// realModel skips under plain -race, where checkptr aborts inside GoMLX.
	fp32 := realModel(t)

	int8Graph := filepath.Join(int8Dir, "model.onnx")
	raw, err := os.ReadFile(int8Graph) // #nosec G304 -- test fixture path from the environment
	if err != nil {
		t.Fatalf("read the int8 graph: %v", err)
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != pinnedInt8ModelSHA256 {
		t.Fatalf("%s has sha256 %x, want the pinned upstream %s: rebuild with scripts/models/build.sh",
			int8Graph, sum, pinnedInt8ModelSHA256)
	}

	// The int8 directory is build scratch with no manifest of its own, so
	// it cannot go through embedded.New. It is built with the same Hugot
	// factory production uses, under the float32 model's manifest, which
	// supplies the head order its config.json labels are validated against.
	m := fp32.Manifest()
	if m == nil {
		t.Fatal("the float32 model has no manifest")
	}
	int8Pipe, err := embedded.HugotPipelineFactory(context.Background(), int8Dir, m)
	if err != nil {
		t.Fatalf("loading the int8 model from %s: %v", int8Dir, err)
	}
	t.Cleanup(func() { _ = int8Pipe.Close() })

	mainT, auxT := m.Calibration.MainThreshold, m.Calibration.AuxThreshold
	temp := m.Calibration.Temperature
	t.Logf("gating against threshold set %q: main>=%.2f AND aux<%.2f, temperature %.2f",
		m.Calibration.ThresholdSet, mainT, auxT, temp)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	signals := []decision.Signal{decision.SignalInjection, decision.SignalHumanDirected}

	// The float32 side goes through Provider.Decide, which is the path
	// production uses, so the gate exercises calibration too.
	scoreFP32 := func(text string) (main, aux float64) {
		t.Helper()
		ds, err := fp32.Decide(ctx, decision.Input{
			Content: text, Direction: decision.DirectionRequest, SourceRole: "user",
		}, signals)
		if err != nil {
			t.Fatalf("fp32 Decide(%q): %v", text, err)
		}
		for _, d := range ds {
			if !d.Answered {
				t.Fatalf("fp32 Decide(%q) left %s unanswered", text, d.Signal)
			}
			switch d.Signal {
			case decision.SignalInjection:
				main = d.Probability
			case decision.SignalHumanDirected:
				aux = d.Probability
			}
		}
		return main, aux
	}

	// The int8 side goes through the pipeline and is calibrated here with
	// the same temperature, so the two numbers are comparable.
	calibrated := func(logit float64) float64 { return 1 / (1 + math.Exp(-logit/temp)) }
	scoreInt8 := func(text string) (main, aux float64) {
		t.Helper()
		rows, err := int8Pipe.Logits(ctx, []string{text})
		if err != nil {
			t.Fatalf("int8 Logits(%q): %v", text, err)
		}
		if len(rows) != 1 || len(rows[0]) != 2 {
			t.Fatalf("int8 Logits(%q) returned %d rows, want 1 row of 2 logits", text, len(rows))
		}
		return calibrated(rows[0][0]), calibrated(rows[0][1])
	}

	// Upstream's rule: flag when the main head clears its threshold and the
	// aux head does not veto.
	decide := func(main, aux float64) bool { return main >= mainT && aux < auxT }

	var worstMain, worstAux float64
	for _, probe := range loadProbes(t) {
		aMain, aAux := scoreFP32(probe.Text)
		bMain, bAux := scoreInt8(probe.Text)

		dMain, dAux := math.Abs(aMain-bMain), math.Abs(aAux-bAux)
		worstMain = math.Max(worstMain, dMain)
		worstAux = math.Max(worstAux, dAux)

		t.Logf("%-16s main fp32=%.6f int8=%.6f d=%.2e | aux fp32=%.6f int8=%.6f d=%.2e",
			probe.ID, aMain, bMain, dMain, aAux, bAux, dAux)

		if dMain > parityTolerance {
			t.Errorf("%s: main head differs by %.2e, over the %.0e tolerance", probe.ID, dMain, parityTolerance)
		}
		if dAux > parityTolerance {
			t.Errorf("%s: aux head differs by %.2e, over the %.0e tolerance", probe.ID, dAux, parityTolerance)
		}
		if decide(aMain, aAux) != decide(bMain, bAux) {
			t.Errorf("%s: the decision flipped (fp32=%v int8=%v)", probe.ID,
				decide(aMain, aAux), decide(bMain, bAux))
		}
	}
	t.Logf("worst delta: main %.2e, aux %.2e (tolerance %.0e)", worstMain, worstAux, parityTolerance)
}

// upstreamCalibration is the subset of upstream's classifier_config.json
// the manifest is built from, under upstream's own key names.
type upstreamCalibration struct {
	OptimalThreshold float64 `json:"optimal_threshold"`
	Calibration      struct {
		TemperatureT      float64 `json:"temperatureT"`
		HighRiskThreshold float64 `json:"highRiskThreshold"`
		ECE               float64 `json:"ece"`
		FittedOn          string  `json:"fitted_on"`
	} `json:"calibration"`
}

func readUpstreamCalibration(t *testing.T, path ...string) upstreamCalibration {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(path...))
	if err != nil {
		t.Fatalf("read the pinned calibration fixture: %v", err)
	}
	var c upstreamCalibration
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse the pinned calibration fixture: %v", err)
	}
	return c
}

// multiheadMainThreshold is not in any upstream JSON file. Defender's
// src/classifiers/tier2-classifier.ts documents it on MultiheadConfig: the
// Tier 2 classifier interprets the model output as [main, aux] and blocks
// iff main >= mainThreshold AND aux < auxThreshold, and "for the bundled
// default model, FP-benchmark validation gives
// { mainThreshold: 0.5, auxThreshold: 0.64 }". Those are compared against
// classifyPair() output, which is sigmoid(logit / T), so they are calibrated
// probabilities, the same scale ELIDA produces.
const multiheadMainThreshold = 0.5

// TestModelParity_ThresholdsComeFromUpstream checks the built manifest
// against the whole pinned upstream file. It is the second half of the
// drift check in fetch.sh: that one catches upstream changing; this one
// catches build.sh reading the wrong JSON path.
func TestModelParity_ThresholdsComeFromUpstream(t *testing.T) {
	dir, ok := embedded.TestModelPath()
	if !ok {
		t.Skipf("set %s to the built float32 model directory", embedded.TestModelPathEnv)
	}
	m, err := embedded.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	upstream := readUpstreamCalibration(t, "..", "..", "scripts", "models", "testdata", "classifier_config.json")

	if m.Calibration.Temperature != upstream.Calibration.TemperatureT {
		t.Errorf("manifest temperature = %v, upstream says %v", m.Calibration.Temperature, upstream.Calibration.TemperatureT)
	}
	// The multi-head main threshold is NOT upstream's optimal_threshold.
	// 0.4 is the single-head operating point; 0.5 is the multi-head one.
	if m.Calibration.MainThreshold != multiheadMainThreshold {
		t.Errorf("manifest main_threshold = %v, want the multi-head %v", m.Calibration.MainThreshold, multiheadMainThreshold)
	}
	if m.Calibration.SingleHeadThreshold != upstream.OptimalThreshold {
		t.Errorf("manifest single_head_threshold = %v, upstream optimal_threshold is %v", m.Calibration.SingleHeadThreshold, upstream.OptimalThreshold)
	}
	if m.Calibration.AuxThreshold != upstream.Calibration.HighRiskThreshold {
		t.Errorf("manifest aux_threshold = %v, upstream highRiskThreshold is %v", m.Calibration.AuxThreshold, upstream.Calibration.HighRiskThreshold)
	}
	if m.Calibration.ECE != upstream.Calibration.ECE {
		t.Errorf("manifest ece = %v, upstream says %v", m.Calibration.ECE, upstream.Calibration.ECE)
	}
	if m.Calibration.FittedOn != upstream.Calibration.FittedOn {
		t.Errorf("manifest fitted_on = %q, upstream says %q", m.Calibration.FittedOn, upstream.Calibration.FittedOn)
	}
}

// TestModelParity_ManifestCalibrationMatchesFixture asserts that the
// calibration the build script wrote into manifest.json is exactly the
// pinned upstream calibration.
//
// The drift check in fetch.sh catches upstream changing a value. This
// catches the build script reading the wrong JSON path and writing a
// plausible-looking wrong value, which is a different and quieter failure:
// an inference pipeline with the wrong temperature produces confident
// nonsense rather than an error.
func TestModelParity_ManifestCalibrationMatchesFixture(t *testing.T) {
	dir, ok := embedded.TestModelPath()
	if !ok {
		t.Skipf("set %s to the built float32 model directory", embedded.TestModelPathEnv)
	}
	m, err := embedded.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The fixture carries UPSTREAM's key names. Mapping them onto ELIDA's
	// manifest fields is what this test is for.
	want := readUpstreamCalibration(t, "..", "..", "internal", "decision", "embedded",
		"testdata", "defender-calibration-v5.json")

	// Every mismatch names both values, so a CI failure is actionable
	// without opening either file.
	type check struct {
		field string
		got   any
		want  any
	}
	for _, c := range []check{
		{"temperature", m.Calibration.Temperature, want.Calibration.TemperatureT},
		{"main_threshold", m.Calibration.MainThreshold, float64(multiheadMainThreshold)},
		{"aux_threshold", m.Calibration.AuxThreshold, want.Calibration.HighRiskThreshold},
		{"single_head_threshold", m.Calibration.SingleHeadThreshold, want.OptimalThreshold},
		{"ece", m.Calibration.ECE, want.Calibration.ECE},
		{"fitted_on", m.Calibration.FittedOn, want.Calibration.FittedOn},
	} {
		if c.got != c.want {
			t.Errorf("manifest calibration %s = %v, but the pinned upstream calibration says %v "+
				"(internal/decision/embedded/testdata/defender-calibration-v5.json; main_threshold is upstream's MultiheadConfig literal).\n"+
				"Either scripts/models/build.sh read the wrong JSON path, or upstream's calibration changed and both fixtures plus the model card need updating under review.",
				c.field, c.got, c.want)
		}
	}

	// The specific confusion this test exists to catch: writing the
	// single-head operating point into the multi-head main threshold. It
	// would lower the detection threshold by about 0.04 on the calibrated
	// scale while looking entirely plausible in the manifest.
	if m.Calibration.MainThreshold == m.Calibration.SingleHeadThreshold {
		t.Errorf("main_threshold and single_head_threshold are both %v: scripts/models/build.sh read optimal_threshold into the main threshold. main_threshold is the multi-head %v; optimal_threshold is the single-head operating point and belongs in single_head_threshold.",
			m.Calibration.MainThreshold, multiheadMainThreshold)
	}
}

// TestModelParity_PackagedTokenizerLengthIs128 pins the sequence length the
// build script documents in tokenizer_config.json.
//
// The key is documentation, not configuration: Hugot's pure-Go tokenizer
// does not read tokenizer_config.json, and ELIDA enforces the 128-token cut
// itself (maxSequenceLength in internal/decision/embedded/hugot.go). The
// test keeps the packaged artifact from advertising upstream's 512.
func TestModelParity_PackagedTokenizerLengthIs128(t *testing.T) {
	dir, ok := embedded.TestModelPath()
	if !ok {
		t.Skipf("set %s to the built float32 model directory", embedded.TestModelPathEnv)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tokenizer_config.json")) // #nosec G304 -- test fixture path from the environment
	if err != nil {
		t.Fatalf("read tokenizer_config.json: %v", err)
	}
	var cfg struct {
		MaxLength      int `json:"max_length"`
		ModelMaxLength int `json:"model_max_length"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse tokenizer_config.json: %v", err)
	}
	if cfg.MaxLength != 128 {
		t.Errorf("max_length = %d, want 128", cfg.MaxLength)
	}
	if cfg.ModelMaxLength != 128 {
		t.Errorf("model_max_length = %d, want 128: upstream ships 512, and build.sh records the 128-token length ELIDA enforces in code", cfg.ModelMaxLength)
	}
}
