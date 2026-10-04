// Package preprocess produces bounded, analysis-only derived
// representations of message content.
//
// Nothing this package returns is ever forwarded to a backend. Derived
// representations exist so a semantic classifier can see through
// normalization, invisible characters, homoglyphs, and simple encodings
// without ELIDA rewriting the traffic it proxies.
//
// Every expansion is bounded: total input bytes, total analysis bytes across
// all representations, representation count, decode recursion depth, and
// decoded-size expansion ratio. Hitting a bound produces a Gap, which is a
// coverage gap — never a violation and never any risk — so partial analysis
// is visible instead of being reported as a clean full scan.
//
// Representations are hashed to prevent cycles and redundant work: two
// different transformation chains that arrive at the same text produce one
// representation, not two.
package preprocess

import (
	"crypto/sha256"
	"fmt"

	"elida/internal/decision"
)

// Transformation names. These strings are recorded on decisions, stored
// events and telemetry, so they are part of the external surface.
const (
	TransformOriginal   = ""
	TransformNFKC       = "nfkc"
	TransformInvisible  = "strip_invisible"
	TransformSkeleton   = "confusable_skeleton"
	TransformURL        = "url_decode"
	TransformHTML       = "html_entity_decode"
	TransformUnicodeEsc = "unicode_escape_decode"
	TransformBase64     = "base64_decode"
	TransformHex        = "hex_decode"
	TransformROT13      = "rot13"
)

// Signals a transformation can raise. These feed inline admission
// (encoded_or_obfuscated) and are recorded as evidence; they are not
// violations on their own.
const (
	SignalZeroWidthRemoved = "zero_width_removed"
	SignalBidiRemoved      = "bidi_removed"
	SignalConfusables      = "confusables_present"
	SignalEncodedPayload   = "encoded_payload"
)

// Representation is one analyzable view of a message.
//
// StartByte and EndByte are offsets into the ORIGINAL content. A
// transformation rewrites the whole string, so a derived representation
// spans its ancestor's full original range; Transform and TransformDepth are
// what distinguish it, not a narrower byte range.
type Representation struct {
	Content        string
	Transform      string
	TransformDepth int
	StartByte      int
	EndByte        int
	Signals        []string
}

// Candidate converts a representation into the scheduler's input type.
func (r Representation) Candidate() decision.Candidate {
	return decision.Candidate{
		Content:        r.Content,
		Transform:      r.Transform,
		TransformDepth: r.TransformDepth,
		StartByte:      r.StartByte,
		EndByte:        r.EndByte,
	}
}

// Budget bounds preprocessing work. Every field must be a positive value.
// MaxInputBytes, MaxAnalysisBytes, MaxRepresentations and MaxExpansionRatio
// are limits whose zero value would otherwise disable the corresponding
// check; Run treats zero or negative on any of those four as fail-safe, not
// unbounded: it runs no transforms at all and reports a GapBudgetUnset gap
// per unset field, so an unconfigured or misconfigured budget never silently
// claims a complete analysis. MaxDecodeDepth is the one exception: zero
// there is itself a valid, fully restrictive bound ("decode nothing"), so it
// is reported as ordinary depth-exhaustion coverage (GapDecodeDepthSpent),
// not as an unset budget.
type Budget struct {
	MaxInputBytes      int
	MaxAnalysisBytes   int
	MaxRepresentations int
	MaxDecodeDepth     int
	MaxExpansionRatio  int
}

// GapReason names why some content was not analyzed.
type GapReason string

const (
	GapInputTruncated       GapReason = "input_truncated"
	GapAnalysisBytesSpent   GapReason = "analysis_bytes_spent"
	GapRepresentationsSpent GapReason = "representations_spent"
	GapDecodeDepthSpent     GapReason = "decode_depth_spent"
	GapExpansionRatio       GapReason = "expansion_ratio_exceeded"
	// GapBudgetUnset marks a Budget field that is zero or negative although
	// the spec requires it to be a positive bound (see Budget). Config
	// validation rejects zero for these fields before a real request ever
	// reaches Run, so this is defense in depth: no transforms run and
	// nothing is claimed to be fully analyzed.
	GapBudgetUnset GapReason = "budget_unset"
)

// Gap records one piece of content that was not analyzed. Gaps are coverage
// facts, not findings.
type Gap struct {
	Reason    GapReason
	Transform string
	Detail    string
}

// Result is everything preprocessing produced for one message.
type Result struct {
	// Representations always starts with the original (possibly truncated).
	Representations []Representation
	Gaps            []Gap
	// Signals is the de-duplicated union of every representation's signals.
	Signals []string
	// AnalysisBytes is the total bytes across all representations.
	AnalysisBytes int
}

// Candidates returns the representations as scheduler candidates, in order.
func (r Result) Candidates() []decision.Candidate {
	out := make([]decision.Candidate, 0, len(r.Representations))
	for _, rep := range r.Representations {
		out = append(out, rep.Candidate())
	}
	return out
}

// HasSignal reports whether any representation raised the named signal.
func (r Result) HasSignal(name string) bool {
	for _, s := range r.Signals {
		if s == name {
			return true
		}
	}
	return false
}

// transform is one registered transformation.
//
// apply returns the transformed text, any signals it raised, and whether the
// result is worth keeping. Returning ok=false (because nothing changed, or
// because a syntax or content check failed) must not consume any budget.
type transform struct {
	name  string
	apply func(s string, b Budget) (out string, signals []string, ok bool)
}

// registry is the ordered transformation list. Order matters only for
// determinism of the output slice; every transformation is tried at every
// depth below MaxDecodeDepth.
//
// Later tasks append to this list. Keep it in this order so test expectations
// about representation ordering stay stable.
func registry() []transform {
	return []transform{
		{name: TransformNFKC, apply: func(s string, _ Budget) (string, []string, bool) {
			out, changed := NormalizeNFKC(s)
			return out, nil, changed
		}},
		{name: TransformInvisible, apply: func(s string, _ Budget) (string, []string, bool) {
			return StripInvisible(s)
		}},
		{name: TransformSkeleton, apply: func(s string, _ Budget) (string, []string, bool) {
			return ConfusableSkeleton(s)
		}},
		{name: TransformURL, apply: func(s string, _ Budget) (string, []string, bool) {
			return DecodeURL(s)
		}},
		{name: TransformHTML, apply: func(s string, _ Budget) (string, []string, bool) {
			return DecodeHTMLEntities(s)
		}},
		{name: TransformUnicodeEsc, apply: func(s string, _ Budget) (string, []string, bool) {
			return DecodeUnicodeEscapes(s)
		}},
	}
}

// Run produces the bounded representation set for content.
//
// It reserves work before allocating output: each candidate transformation is
// checked against the representation count, the aggregate analysis byte
// budget, and the expansion ratio before its output is retained.
//
// If MaxInputBytes, MaxAnalysisBytes, MaxRepresentations or MaxExpansionRatio
// is zero or negative, Run fails safe: it runs no transforms, returns only
// the untruncated original, and reports one GapBudgetUnset gap per unset
// field. See Budget.
func Run(content string, b Budget) Result {
	if gaps := unsetBudgetGaps(b); len(gaps) > 0 {
		return finish(Result{
			Representations: []Representation{{
				Content:        content,
				Transform:      TransformOriginal,
				TransformDepth: 0,
				StartByte:      0,
				EndByte:        len(content),
			}},
			Gaps:          gaps,
			AnalysisBytes: len(content),
		})
	}

	var res Result

	// The original, truncated to the input budget. Truncation is a gap.
	original := content
	if b.MaxInputBytes > 0 && len(original) > b.MaxInputBytes {
		original = truncateUTF8(original, b.MaxInputBytes)
		res.Gaps = append(res.Gaps, Gap{
			Reason:    GapInputTruncated,
			Transform: TransformOriginal,
			Detail:    fmt.Sprintf("content is %d bytes; analyzed the first %d", len(content), len(original)),
		})
	}

	res.Representations = []Representation{{
		Content:        original,
		Transform:      TransformOriginal,
		TransformDepth: 0,
		StartByte:      0,
		EndByte:        len(original),
	}}
	res.AnalysisBytes = len(original)

	if original == "" {
		return res
	}

	// Cycle detection: two chains that reach the same text are one
	// representation. The original counts, so an idempotent transformation
	// cannot re-add it.
	seen := map[[32]byte]bool{sha256.Sum256([]byte(original)): true}

	// Fetched once: the registry is immutable for the duration of one Run,
	// and every depth level and every post-loop exhaustion check walks the
	// same list.
	transforms := registry()

	// Breadth-first by depth so shallower chains win the budget.
	frontier := []int{0} // indices into res.Representations
	for depth := 1; depth <= b.MaxDecodeDepth; depth++ {
		var next []int
		for _, parentIdx := range frontier {
			parent := res.Representations[parentIdx]
			for _, tf := range transforms {
				out, signals, ok := tf.apply(parent.Content, b)
				if !ok || out == "" {
					continue
				}
				if b.MaxExpansionRatio > 0 && len(out) > len(parent.Content)*b.MaxExpansionRatio {
					res.Gaps = append(res.Gaps, Gap{
						Reason:    GapExpansionRatio,
						Transform: chain(parent.Transform, tf.name),
						Detail:    fmt.Sprintf("%d bytes from %d exceeds the %dx expansion limit", len(out), len(parent.Content), b.MaxExpansionRatio),
					})
					continue
				}
				h := sha256.Sum256([]byte(out))
				if seen[h] {
					continue // cycle or duplicate chain; no budget consumed
				}
				if b.MaxRepresentations > 0 && len(res.Representations) >= b.MaxRepresentations {
					res.Gaps = append(res.Gaps, Gap{
						Reason:    GapRepresentationsSpent,
						Transform: chain(parent.Transform, tf.name),
						Detail:    fmt.Sprintf("representation limit of %d reached", b.MaxRepresentations),
					})
					return finish(res)
				}
				if b.MaxAnalysisBytes > 0 && res.AnalysisBytes+len(out) > b.MaxAnalysisBytes {
					res.Gaps = append(res.Gaps, Gap{
						Reason:    GapAnalysisBytesSpent,
						Transform: chain(parent.Transform, tf.name),
						Detail:    fmt.Sprintf("adding %d bytes would exceed the %d-byte analysis budget", len(out), b.MaxAnalysisBytes),
					})
					return finish(res)
				}

				seen[h] = true
				res.AnalysisBytes += len(out)
				res.Representations = append(res.Representations, Representation{
					Content:        out,
					Transform:      chain(parent.Transform, tf.name),
					TransformDepth: depth,
					StartByte:      parent.StartByte,
					EndByte:        parent.EndByte,
					Signals:        append(append([]string(nil), parent.Signals...), signals...),
				})
				next = append(next, len(res.Representations)-1)
			}
		}
		if len(next) == 0 {
			break
		}
		frontier = next
	}

	// Anything still expandable at the depth limit is a gap. This also
	// covers MaxDecodeDepth == 0: the loop above never runs, frontier is
	// still just the original at depth 0, and 0 < 0 is false, so a
	// transformable original is correctly reported as depth-exhausted
	// rather than silently dropped.
	if len(frontier) > 0 {
		for _, idx := range frontier {
			rep := res.Representations[idx]
			if rep.TransformDepth < b.MaxDecodeDepth {
				continue
			}
			for _, tf := range transforms {
				if _, _, ok := tf.apply(rep.Content, b); ok {
					res.Gaps = append(res.Gaps, Gap{
						Reason:    GapDecodeDepthSpent,
						Transform: chain(rep.Transform, tf.name),
						Detail:    fmt.Sprintf("decode depth limit of %d reached", b.MaxDecodeDepth),
					})
					break
				}
			}
		}
	}

	return finish(res)
}

// unsetBudgetGaps reports one GapBudgetUnset per field in b that the spec
// requires to be positive but is zero or negative. MaxDecodeDepth is
// deliberately excluded: zero is a valid, fully restrictive bound there
// (see Budget), not an unset field.
func unsetBudgetGaps(b Budget) []Gap {
	fields := []struct {
		name string
		val  int
	}{
		{"max_input_bytes", b.MaxInputBytes},
		{"max_analysis_bytes", b.MaxAnalysisBytes},
		{"max_representations", b.MaxRepresentations},
		{"max_expansion_ratio", b.MaxExpansionRatio},
	}
	var gaps []Gap
	for _, f := range fields {
		if f.val <= 0 {
			gaps = append(gaps, Gap{
				Reason:    GapBudgetUnset,
				Transform: TransformOriginal,
				Detail:    fmt.Sprintf("%s is %d; a positive bound is required, so no transforms were run", f.name, f.val),
			})
		}
	}
	return gaps
}

// finish computes the signal union. Called on every return path from Run.
func finish(res Result) Result {
	seen := map[string]bool{}
	for _, rep := range res.Representations {
		for _, s := range rep.Signals {
			if !seen[s] {
				seen[s] = true
				res.Signals = append(res.Signals, s)
			}
		}
	}
	return res
}

// chain joins a parent transformation chain with a new transformation name.
func chain(parent, name string) string {
	if parent == TransformOriginal {
		return name
	}
	return parent + ">" + name
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8ValidBoundary(s, n) {
		n--
	}
	return s[:n]
}

// utf8ValidBoundary reports whether index i is a rune boundary in s.
func utf8ValidBoundary(s string, i int) bool {
	if i == 0 || i == len(s) {
		return true
	}
	return s[i]&0xC0 != 0x80
}
