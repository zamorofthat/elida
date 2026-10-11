package embedded

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unicode/utf8"

	"github.com/gomlx/go-huggingface/tokenizers/api"
	"github.com/gomlx/go-huggingface/tokenizers/hftokenizer"
	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/backends"
	"github.com/knights-analytics/hugot/pipelines"

	"elida/internal/decision"
)

// TestModelPathEnv names the environment variable that points at a real
// packaged model. Tests that need one skip when it is unset, so a developer
// without the 90 MiB artifact still gets a green suite.
const TestModelPathEnv = "ELIDA_TEST_MODEL_PATH"

// TestModelPath returns the configured real-model path, if any.
func TestModelPath() (string, bool) {
	p := os.Getenv(TestModelPathEnv)
	return p, p != ""
}

// ErrScoreOutOfRange means the backend returned a score outside [0,1] where
// a sigmoid output was expected, which would make the inversion below
// meaningless.
var ErrScoreOutOfRange = errors.New("embedded: classification score is outside [0,1]")

// sigmoidEpsilon clamps scores away from 0 and 1 before inverting the
// sigmoid. float32 saturates well before the true logit does, so without
// this a confident score inverts to +/-Inf and the temperature division
// produces Inf rather than a probability.
//
// 1e-6 corresponds to a logit of about +/-13.8, far outside anything a
// calibrated head produces, so the clamp never alters a real decision.
const sigmoidEpsilon = 1e-6

// maxSequenceLength is the longest token sequence, special tokens included,
// that one inference may carry. It is the window size the model was trained
// at (tokenizer_config.json max_length) and the size inline admission is
// costed against.
//
// It is enforced HERE, not by Hugot. In v0.8.1 the pure-Go path does not
// truncate or pad at all:
//
//   - backends/tokenizer_go.go passes EncodeOptions.MaxLen =
//     max_position_embeddings (512) to go-huggingface, but go-huggingface
//     v0.4.13's hftokenizer stores MaxLen and never reads it, and it ignores
//     the truncation and padding sections of tokenizer.json.
//   - tokenizer_config.json is not read by the Go tokenizer at all, so
//     neither max_length nor model_max_length has any effect.
//   - The Go backend does not bucket the sequence dimension
//     (createInputTensorsGoMLX pads sequences only for XLA), so the graph
//     runs at exactly the token count of the input.
//
// So cost scales with the real token count, and an input longer than 512
// tokens would run past the position embeddings. truncate() cuts every input
// to this length before it reaches Hugot.
//
// pipelines.WithFixedPadding(n) was considered and rejected: it pads every
// short input up to n (forfeiting the dynamic-length saving) and truncates
// by dropping trailing tokens, [SEP] included, which is not how the model
// was trained.
const maxSequenceLength = 128

// classifyFunc is one RunPipeline call. It is a field rather than a direct
// method call so the adapter's batching, label mapping, and panic handling
// are testable without a model.
type classifyFunc func(ctx context.Context, texts []string) (*pipelines.TextClassificationOutput, error)

// hugotPipeline adapts Hugot's text-classification pipeline to Pipeline.
//
// One session and one pipeline are shared across callers. The scheduler owns
// the concurrency bound. Hugot's text-classification RunPipeline holds no
// per-call state on the pipeline (timings are atomic, the batch is per call)
// and the go-huggingface tokenizer is read-only after construction, so
// inference runs concurrently under a read lock; the write lock is only for
// Close, so a pipeline is never destroyed under an in-flight call.
//
// Cancellation is not an abort. Hugot's Go backend answers a canceled
// context by returning early while the GoMLX computation keeps running on
// its own goroutine, and RunPipeline then finalizes the input tensors under
// it; a Close after that destroys the backend under a live computation and
// crashes the process. So GoMLX never sees a cancelable context: every
// inference runs to completion while the caller stays blocked, holding its
// scheduler worker slot for exactly as long as the CPU is busy, and only
// then reports ctx.Err(). The inline deadline therefore bounds what a
// request waits for, not the CPU an inference spends.
//
// One inference is not one CPU. GoMLX's pure-Go backend executes each graph
// over a shared intra-op worker pool (2 x GOMAXPROCS); a warm 128-token
// inference measured cpu/wall of about 4.7 on an 8-core M1 Pro. A scheduler
// worker slot (decision.max_concurrency) is therefore several cores, not
// one. Capping intra-op parallelism is a follow-up pending a Hugot option
// that passes GoMLX's parallelism setting through.
type hugotPipeline struct {
	mu       sync.RWMutex
	session  *hugot.Session // nil in tests; Destroy is skipped
	classify classifyFunc   // nil once closed
	tok      *hftokenizer.Tokenizer
	specials int // special tokens the post-processor adds to one sequence
	heads    []string
	headIdx  map[string]int
	tokRatio int
}

// newHugotPipeline wires an adapter around a classifier. tok may be nil, in
// which case inputs are passed through untruncated (white-box tests only).
func newHugotPipeline(heads []string, classify classifyFunc, tok *hftokenizer.Tokenizer) *hugotPipeline {
	idx := make(map[string]int, len(heads))
	for i, h := range heads {
		idx[h] = i
	}
	return &hugotPipeline{
		classify: classify,
		tok:      tok,
		specials: 2, // [CLS] and [SEP]; replaced by the measured value when a tokenizer is loaded
		heads:    append([]string(nil), heads...),
		headIdx:  idx,
		// Bytes per token for this WordPiece vocabulary on English text.
		// CountTokens only needs to be close: it drives windowing and the
		// inline token budget, and truncate() enforces the hard limit.
		tokRatio: 4,
	}
}

// HugotPipelineFactory builds the pure-Go inference pipeline for a verified
// model directory. It is the default Options.NewPipeline.
func HugotPipelineFactory(ctx context.Context, dir string, m *Manifest) (Pipeline, error) {
	// The session derives its lifetime context from ctx. A startup context
	// that is canceled or times out after New returns would otherwise fail
	// every later inference with "context canceled", so the session keeps
	// ctx's values but not its cancellation. Close ends it.
	session, err := hugot.NewGoSession(context.WithoutCancel(ctx))
	if err != nil {
		return nil, fmt.Errorf("embedded: creating the pure-Go Hugot session: %w", err)
	}

	pipe, err := session.NewPipeline(hugot.TextClassificationConfig{
		ModelPath: dir,
		Name:      "injection",
		Options: []backends.PipelineOption[*pipelines.TextClassificationPipeline]{
			// Multi-label: the two heads are independent, not a softmax over
			// classes. A high human_directed does not mean a low injection;
			// it means the directive is aimed at a person, which is the
			// veto this model exists to express.
			pipelines.WithMultiLabel(),
			// Hugot v0.8.1 has no raw-logit option: postprocess always
			// applies SIGMOID or SOFTMAX and errors on anything else. The
			// sigmoid here is uncalibrated; Logits() inverts it and
			// calibrate() re-applies it with the model's temperature. If a
			// later Hugot exposes raw logits, use that and delete the
			// inversion.
			pipelines.WithSigmoid(),
		},
	})
	if err != nil {
		_ = session.Destroy()
		return nil, fmt.Errorf("embedded: building the text-classification pipeline from %s: %w", dir, err)
	}

	if lerr := validateHeadLabels(pipe.Model.IDLabelMap, m.HeadOrder); lerr != nil {
		_ = session.Destroy()
		return nil, lerr
	}

	tok, specials, err := loadTruncationTokenizer(dir)
	if err != nil {
		_ = session.Destroy()
		return nil, err
	}

	h := newHugotPipeline(m.HeadOrder, pipe.RunPipeline, tok)
	h.session = session
	h.specials = specials
	return h, nil
}

// validateHeadLabels checks the model's id2label (from config.json) against
// the manifest's head order before startup succeeds. Output index i must
// carry a label that normalizes to HeadOrder[i], and there must be exactly
// one label per head. The adapter maps by label, so a mismatch would not
// swap heads silently; it would load "healthy" and then fail every
// inference, or let two labels for one head overwrite each other. Failing
// here routes it through the provider's Required/degraded startup handling
// instead.
func validateHeadLabels(idLabel map[int]string, heads []string) error {
	if len(idLabel) != len(heads) {
		return fmt.Errorf("embedded: config.json id2label has %d labels, the manifest declares %d heads", len(idLabel), len(heads))
	}
	for i, want := range heads {
		label, ok := idLabel[i]
		if !ok {
			return fmt.Errorf("embedded: config.json id2label has no label for output %d (head %q)", i, want)
		}
		got, ok := normalizeHeadLabel(label)
		if !ok || got != want {
			return fmt.Errorf("embedded: config.json id2label[%d] = %q, which does not name head %q", i, label, want)
		}
	}
	return nil
}

// loadTruncationTokenizer builds a private go-huggingface tokenizer from the
// same tokenizer.json Hugot loaded, used only to find where to cut an input.
// It is separate from Hugot's instance so configuring it (spans on, special
// tokens off) cannot change what Hugot feeds the model.
func loadTruncationTokenizer(dir string) (*hftokenizer.Tokenizer, int, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "tokenizer.json")) // #nosec G304 -- verified model directory
	if err != nil {
		return nil, 0, fmt.Errorf("embedded: reading tokenizer.json: %w", err)
	}
	tok, err := hftokenizer.NewFromContent(nil, raw)
	if err != nil {
		return nil, 0, fmt.Errorf("embedded: parsing tokenizer.json: %w", err)
	}
	// Measure the special-token overhead the way Hugot will encode: with
	// the post-processor on, an empty input is exactly the specials.
	if err := tok.With(api.EncodeOptions{AddSpecialTokens: true}); err != nil {
		return nil, 0, fmt.Errorf("embedded: configuring the tokenizer: %w", err)
	}
	specials := len(tok.Encode(""))
	if specials >= maxSequenceLength {
		return nil, 0, fmt.Errorf("embedded: tokenizer adds %d special tokens, leaving no room in a %d-token sequence", specials, maxSequenceLength)
	}
	if err := tok.With(api.EncodeOptions{IncludeSpans: true}); err != nil {
		return nil, 0, fmt.Errorf("embedded: configuring the tokenizer: %w", err)
	}
	return tok, specials, nil
}

// truncate cuts text so that Hugot's encoding of it, special tokens
// included, is at most maxSequenceLength tokens. It keeps the head of the
// text, matching the model's right-side truncation, and cuts on a token
// boundary in the original bytes.
//
// The tokenizer is never shown more than maxTruncationProbeBytes at once.
// go-huggingface v0.4.13's BERT pre-tokenizer is quadratic in input length
// (pretokenizer.go computes each rune's byte offset as
// len(string(runes[:i]))): a 128-token text encodes in about 2 ms, 256
// tokens in 8 ms, 512 in 32 ms on an M1 Pro. Hugot runs that same tokenizer
// over whatever it is given, so an unbounded input would cost unbounded CPU
// before inference even starts. Probing a growing prefix keeps both
// tokenizer passes bounded.
//
// Pathological input can collapse the scored text: invalid UTF-8 made of
// continuation bytes has no rune boundary to cut on, and token-cheap
// filler longer than maxTruncationProbeBytes pushes anything after it out
// of the probe. Logits refuses to score an empty result of a non-empty
// input (ErrNothingToScore), and exact CountTokens keeps scheduler
// windows small enough that this cut is a backstop, not the normal path.
//
// Tokenizer parity: for English the kept tokens match HF tokenizers'
// right truncation at 128 exactly (the parity test pins two >128-token
// texts). For some multibyte text the back-off pass can stop one token
// short of the limit (a Japanese+emoji probe kept 127 where HF keeps 128,
// with the first 126 identical); that is a documented limitation.
//
// The result is not reported upward: Pipeline.Logits returns logits only,
// and Hugot itself exposes no truncation flag (it never truncates). A
// window the scheduler joined past its token budget is therefore scored on
// its first maxSequenceLength tokens with no marker on the Decision. That
// is a known limitation, recorded in the task report.
func (h *hugotPipeline) truncate(text string) string {
	if h.tok == nil {
		return text
	}
	budget := maxSequenceLength - h.specials
	// Every content token covers at least one byte of the original text,
	// so a text no longer than the budget in bytes always fits.
	if len(text) <= budget {
		return text
	}

	// Find a prefix holding at least budget tokens, starting from a
	// generous bytes-per-token guess and doubling up to the probe cap.
	for probe := min(budget*truncationProbeBytesPerToken, maxTruncationProbeBytes); ; probe = min(probe*2, maxTruncationProbeBytes) {
		prefix := runePrefix(text, probe)
		enc := h.tok.EncodeWithAnnotations(prefix)
		whole := len(prefix) == len(text)
		if whole && len(enc.IDs) <= budget {
			return text
		}
		if len(enc.IDs) >= budget && len(enc.Spans) >= budget {
			text = runePrefix(prefix, enc.Spans[budget-1].End)
			break
		}
		if whole || probe >= maxTruncationProbeBytes {
			// Few tokens over many bytes (long words become a single
			// [UNK]): the capped prefix is what gets scored.
			text = prefix
			break
		}
	}

	// Cutting a word mid-way can re-tokenize into more pieces than the
	// prefix had, so verify and back off. Each pass strictly shortens the
	// text; a pass or two covers any real input.
	target := budget
	for range 16 {
		enc := h.tok.EncodeWithAnnotations(text)
		if len(enc.IDs) <= budget {
			return text
		}
		cut := len(text) / 2
		if target > 0 && len(enc.Spans) >= target {
			if end := enc.Spans[target-1].End; end > 0 && end < len(text) {
				cut = end
			}
		}
		text = runePrefix(text, cut)
		target-- // leave room for a boundary re-split on the next pass
	}
	// Unreachable in practice: every content token covers at least one
	// byte, so a budget-byte prefix always fits.
	return runePrefix(text, budget)
}

// truncationProbeBytesPerToken sizes the first prefix truncate() tokenizes.
// English WordPiece averages four to six bytes per token, so eight gives a
// prefix that usually holds the whole budget in one pass.
const truncationProbeBytesPerToken = 8

// maxTruncationProbeBytes caps the prefix truncate() will tokenize, and so
// the longest text Hugot is ever handed.
const maxTruncationProbeBytes = 4096

// runePrefix returns the longest prefix of s that is at most n bytes and
// does not split a UTF-8 sequence.
func runePrefix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Logits runs the pipeline once per text and returns one raw logit per head,
// in manifest head order.
//
// Head order is resolved by LABEL, not by position in the backend's output.
// The manifest asserts the order is [injection, human_directed]; matching on
// labels means a backend that reorders its output cannot silently swap the
// detector and the veto.
func (h *hugotPipeline) Logits(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.classify == nil {
		return nil, errors.New("embedded: pipeline is closed")
	}

	// One text per RunPipeline call. See maxBatch: a larger batch panics
	// inside the backend in v0.8.1.
	out := make([][]float64, 0, len(texts))
	for _, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		scored, err := h.prepare(text)
		if err != nil {
			return nil, err
		}
		batch := []string{scored}
		if gerr := batchGuard(len(batch)); gerr != nil {
			return nil, gerr
		}
		res, err := h.runOne(ctx, batch)
		if err != nil {
			// A recovered panic is the provider's fault whatever the
			// caller's context did; anything else after the caller gave up
			// is a budget outcome.
			if !errors.Is(err, errInferencePanic) {
				if cerr := ctx.Err(); cerr != nil {
					return nil, cerr
				}
			}
			return nil, err
		}
		// The computation ran to completion (see runOne). A caller whose
		// context ended meanwhile gets ctx.Err(), never a late score.
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		if res == nil || len(res.ClassificationOutputs) != 1 {
			n := 0
			if res != nil {
				n = len(res.ClassificationOutputs)
			}
			return nil, fmt.Errorf("embedded: pipeline returned %d outputs for 1 input", n)
		}
		row, err := h.row(res.ClassificationOutputs[0])
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// ErrNothingToScore means truncation or the tokenizer byte cap reduced a
// non-empty input to nothing, as with text made only of invalid UTF-8
// continuation bytes. Scoring "" would report a confident answer about text
// the model never saw, so the window stays unanswered: unknown, never safe.
//
// It describes the input, not the provider. The provider does not count it
// toward the circuit breaker (an attacker could otherwise open the breaker,
// and so blind detection, by sending junk) and reports it in
// Health.InputRejected instead.
var ErrNothingToScore error = inputRejectedError{}

// inputRejectedError is ErrNothingToScore's type. A distinct type makes the
// scheduler's content-free error_class log field read
// "embedded.inputRejectedError" rather than a generic string error.
type inputRejectedError struct{}

func (inputRejectedError) Error() string {
	return "embedded: input rejected: truncation left nothing to score"
}

// prepare truncates one input for inference. Tokenizer panics are recovered
// into errInferencePanic, like backend panics, and the error never carries
// the input.
func (h *hugotPipeline) prepare(text string) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = ""
			err = panicError("tokenizer", r)
		}
	}()
	out = h.truncate(text)
	if out == "" && text != "" {
		return "", ErrNothingToScore
	}
	return out, nil
}

// panicError converts a recovered panic value into an error wrapping
// errInferencePanic without echoing content: a runtime.Error keeps its
// message (it names a bounds or nil failure), anything else is reported by
// type only.
func panicError(where string, r any) error {
	if re, ok := r.(runtime.Error); ok {
		return fmt.Errorf("%w in the %s: %s", errInferencePanic, where, re.Error())
	}
	return fmt.Errorf("%w in the %s: panic value of type %T", errInferencePanic, where, r)
}

// runOne makes exactly one RunPipeline call, converting a backend panic into
// an error that wraps errInferencePanic so the provider counts it and the
// breaker sees it.
//
// The error never carries input text (see panicError).
//
// The backend gets context.WithoutCancel(ctx): Hugot's cancellation returns
// early while GoMLX keeps computing on tensors RunPipeline has already
// released, so the only safe cancellation is none. The caller stays blocked
// until the computation finishes; Logits reports ctx.Err() afterwards.
func (h *hugotPipeline) runOne(ctx context.Context, batch []string) (res *pipelines.TextClassificationOutput, err error) {
	defer func() {
		if r := recover(); r != nil {
			res = nil
			err = panicError("Hugot backend", r)
		}
	}()
	res, err = h.classify(context.WithoutCancel(ctx), batch)
	if err != nil {
		return nil, fmt.Errorf("embedded: inference failed: %w", err)
	}
	return res, nil
}

// row maps one input's labeled scores onto manifest head order as raw
// logits.
func (h *hugotPipeline) row(outputs []pipelines.ClassificationOutput) ([]float64, error) {
	row := make([]float64, len(h.heads))
	filled := make([]bool, len(h.heads))
	for _, o := range outputs {
		head, ok := normalizeHeadLabel(o.Label)
		if !ok {
			continue // a label the manifest does not claim: ignore it
		}
		pos, ok := h.headIdx[head]
		if !ok {
			continue
		}
		logit, err := invertSigmoid(float64(o.Score))
		if err != nil {
			return nil, fmt.Errorf("embedded: head %q: %w", head, err)
		}
		row[pos] = logit
		filled[pos] = true
	}
	for pos, ok := range filled {
		if !ok {
			return nil, fmt.Errorf("embedded: the model produced no score for head %q", h.heads[pos])
		}
	}
	return row, nil
}

// batchGuard exists so the maxBatch invariant is checked, not just
// documented. Any future change that passes more than one text to
// RunPipeline fails this.
func batchGuard(n int) error {
	if n > maxBatch {
		return fmt.Errorf("embedded: refusing a batch of %d: Hugot v0.8.1 panics inside the GoMLX backend above a small batch size (maxBatch=%d)", n, maxBatch)
	}
	return nil
}

// normalizeHeadLabel maps a model's label string onto a decision signal
// name.
func normalizeHeadLabel(label string) (string, bool) {
	switch label {
	case "INJECTION", "injection", "LABEL_0":
		return string(decision.SignalInjection), true
	case "AUX", "aux", "HUMAN_DIRECTED", "human_directed", "LABEL_1":
		// "AUX" is what the packaged Defender config.json labels head 1.
		// The other spellings are accepted so a relabeled rebuild does not
		// silently stop mapping the veto head.
		return string(decision.SignalHumanDirected), true
	}
	return "", false
}

// invertSigmoid recovers the logit from an uncalibrated sigmoid output.
//
// This exists because the backend applies a sigmoid without the model's
// calibration temperature. Inverting and re-applying with the temperature is
// mathematically identical to never having applied the first sigmoid, and it
// keeps all calibration in one place: calibrate().
func invertSigmoid(s float64) (float64, error) {
	if math.IsNaN(s) {
		return 0, fmt.Errorf("%w: NaN", ErrScoreOutOfRange)
	}
	if s < 0 || s > 1 {
		return 0, fmt.Errorf("%w: %v", ErrScoreOutOfRange, s)
	}
	if s < sigmoidEpsilon {
		s = sigmoidEpsilon
	}
	if s > 1-sigmoidEpsilon {
		s = 1 - sigmoidEpsilon
	}
	return math.Log(s / (1 - s)), nil
}

// CountTokens counts the tokens one inference over text costs, special
// tokens included, for windowing and the inline token budget.
//
// It is exact for text up to maxTruncationProbeBytes, so a scheduler window
// of at most maxSequenceLength counted tokens reaches the model whole and
// truncate() stays a backstop. A byte estimate is not enough: JSON, code and
// punctuation run at one to one and a half bytes per token, so a bytes/4
// window can hold three times the tokens the model sees, and whatever an
// attacker puts after a token-dense prefix would go unscored.
//
// Above the cap it returns len(text): a content token is never shorter than
// one byte, so that never undercounts, it forces the scheduler to split, and
// it keeps the quadratic tokenizer from seeing more than the cap. The count
// stays monotonic in prefix length, which the scheduler's hard split
// assumes. A tokenizer panic also yields len(text).
//
// Documented costs of exactness:
//   - Each call runs the tokenizer: about 2 ms for a 128-token text on an
//     M1 Pro (go-huggingface v0.4.13; quadratic in length, bounded by the
//     cap). The scheduler counts every sentence and every window, so a long
//     message pays this several times; caching is a follow-up.
//   - The count includes the special tokens ([CLS], [SEP]) on every call,
//     and the scheduler sums per-sentence counts, so a window packs slightly
//     fewer sentences than its exact joined count would allow. This is
//     conservative: windows never exceed what one inference sees.
func (h *hugotPipeline) CountTokens(text string) (n int) {
	if text == "" {
		return 0
	}
	if h.tok != nil {
		if len(text) > maxTruncationProbeBytes {
			return len(text)
		}
		defer func() {
			if recover() != nil {
				n = len(text)
			}
		}()
		return len(h.tok.Encode(text)) + h.specials
	}
	// No tokenizer (white-box tests only): estimate.
	ratio := h.tokRatio
	if ratio <= 0 {
		ratio = 4
	}
	return (len(text) + ratio - 1) / ratio
}

// Close destroys the pipeline and session. It takes the write lock, so it
// waits for every in-flight inference to finish (including ones whose caller
// has already been canceled, since those run to completion) and never tears
// GoMLX down under a live computation. It blocks for at most one inference
// duration per in-flight call. It is safe to call more than once.
func (h *hugotPipeline) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.classify = nil
	if h.session == nil {
		return nil
	}
	err := h.session.Destroy()
	h.session = nil
	return err
}

var _ Pipeline = (*hugotPipeline)(nil)
