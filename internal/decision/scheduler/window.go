// Package scheduler splits content into bounded windows, runs a
// decision.Provider inside one global deadline, and composes the
// decision.Assessment.
//
// It is the only component that knows how many windows were eligible, how
// many completed inline versus went to the bounded async queue, and whether
// the request has already been forwarded. Coverage and ProtectionScope are
// therefore set here and nowhere else.
package scheduler

import (
	"sort"
	"strings"

	"elida/internal/decision"
)

// DefaultWindowTokens is the maximum model tokens per window.
//
// It is a constant, not a config key: 128 tokens is where this model's
// signal stays usable. Feeding a 1,268-character benign text as one input
// scored 0.905 in the feasibility spike, so a longer window does not mean
// more coverage, it means a meaningless score. How many of these windows run
// inline is operator-tunable (decision.max_inline_windows); how long one
// window is, is not.
const DefaultWindowTokens = 128

// WindowedText is one window: where it came from, its text, and its token
// count.
type WindowedText struct {
	Window decision.Window
	Text   string
	Tokens int
}

// isSentenceTerminator reports whether b ends a sentence when followed by
// whitespace or by the end of the string.
func isSentenceTerminator(b byte) bool {
	return b == '.' || b == '!' || b == '?'
}

// splitSentences splits s into sentences, keeping all trailing whitespace
// attached to the sentence it follows so the pieces reassemble into s
// exactly.
//
// A terminator counts only when followed by whitespace or end-of-string,
// which keeps "3.50" and "example.com" in one piece. A newline always ends a
// sentence: in chat content a line break is a stronger boundary than a
// period.
func splitSentences(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
			continue
		}
		if !isSentenceTerminator(c) {
			continue
		}
		// Consume a run of terminators ("...", "?!").
		j := i
		for j+1 < len(s) && isSentenceTerminator(s[j+1]) {
			j++
		}
		if j+1 >= len(s) {
			break // trailing terminator: the tail below picks it up
		}
		if !isSpace(s[j+1]) {
			i = j
			continue // not a boundary: "3.50", "example.com"
		}
		// Absorb the whitespace run that follows.
		k := j + 1
		for k < len(s) && isSpace(s[k]) && s[k] != '\n' {
			k++
		}
		if k < len(s) && s[k] == '\n' {
			k++
		}
		out = append(out, s[start:k])
		start = k
		i = k - 1
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}

// SplitWindows splits a candidate into sentence-aligned windows of at most
// maxTokens model tokens.
//
// Byte ranges. For the original candidate (Transform == "") a window's span
// is its real absolute range in the original content. For a derived
// candidate the span is the ancestor's full range, because a decode cannot
// be byte-mapped in reverse; Transform and TransformDepth are what identify
// such a window.
//
// Windows tile the content: they are contiguous, non-overlapping, and
// concatenating their text reproduces the candidate exactly. A single
// sentence longer than maxTokens is hard-split at a word boundary.
//
// maxTokens <= 0 falls back to DefaultWindowTokens. Every window's Tokens is
// the counter's own count of the window text, and a window over budget
// (possible only with a non-additive counter) is hard-split. A single rune
// that alone exceeds maxTokens cannot be split without corrupting UTF-8 and
// is emitted whole, so it necessarily exceeds the budget. A counter that
// returns 0 for non-empty text reports everything as fitting, so the content
// becomes a single window.
func SplitWindows(c decision.Candidate, tc decision.TokenCounter, maxTokens int) []WindowedText {
	if c.Content == "" {
		return nil
	}
	if maxTokens <= 0 {
		maxTokens = DefaultWindowTokens
	}
	derived := c.Transform != ""

	mkWindow := func(localStart, localEnd int, text string, tokens int) WindowedText {
		w := decision.Window{Transform: c.Transform, TransformDepth: c.TransformDepth}
		if derived {
			w.StartByte, w.EndByte = c.StartByte, c.EndByte
		} else {
			w.StartByte, w.EndByte = c.StartByte+localStart, c.StartByte+localEnd
		}
		return WindowedText{Window: w, Text: text, Tokens: tokens}
	}

	var out []WindowedText
	sentences := splitSentences(c.Content)

	var bufStart, bufEnd int
	var bufTokens int
	// emit appends the span [from,to) as one window, re-counting its text so
	// Tokens is truthful for any counter. A span over budget is hard-split.
	emit := func(from, to int) {
		text := c.Content[from:to]
		if n := tc.CountTokens(text); n <= maxTokens {
			out = append(out, mkWindow(from, to, text, n))
			return
		}
		for _, piece := range hardSplit(text, tc, maxTokens) {
			out = append(out, mkWindow(from, from+len(piece), piece, tc.CountTokens(piece)))
			from += len(piece)
		}
	}
	flush := func() {
		if bufEnd > bufStart {
			emit(bufStart, bufEnd)
		}
		bufStart, bufTokens = bufEnd, 0
	}

	offset := 0
	for _, sentence := range sentences {
		sStart, sEnd := offset, offset+len(sentence)
		offset = sEnd
		sTokens := tc.CountTokens(sentence)

		if sTokens > maxTokens {
			// The sentence alone busts the budget. Flush what we have, then
			// hard-split the sentence at word boundaries.
			flush()
			emit(sStart, sEnd)
			bufStart, bufEnd = sEnd, sEnd
			continue
		}

		if bufTokens+sTokens > maxTokens && bufEnd > bufStart {
			flush()
		}
		bufEnd = sEnd
		bufTokens += sTokens
	}
	flush()
	return out
}

// hardSplit cuts s into pieces of at most maxTokens tokens, preferring word
// boundaries. The pieces concatenate back to s and every cut is on a rune
// boundary, so a single rune that alone exceeds maxTokens is emitted whole.
//
// Tokenizer work is proportional to the pieces produced, not to the
// remainder: each piece is found by doubling a prefix until it stops fitting
// and then bisecting, so every CountTokens call sees at most about twice a
// piece. The counter is assumed monotonic in prefix length.
func hardSplit(s string, tc decision.TokenCounter, maxTokens int) []string {
	var out []string
	for len(s) > 0 {
		// Gallop: lo is a prefix known to fit, hi one known not to (or len).
		lo, hi := 0, alignUp(s, maxTokens)
		for hi < len(s) && tc.CountTokens(s[:hi]) <= maxTokens {
			lo = hi
			hi = alignUp(s, hi*2)
		}
		if hi >= len(s) {
			hi = len(s)
			if tc.CountTokens(s) <= maxTokens {
				out = append(out, s)
				break
			}
		}
		// Bisect for the longest fitting rune-aligned prefix in [lo, hi).
		for {
			mid := alignDown(s, (lo+hi)/2)
			if mid <= lo {
				mid = alignUp(s, lo+1)
			}
			if mid >= hi {
				break
			}
			if tc.CountTokens(s[:mid]) <= maxTokens {
				lo = mid
			} else {
				hi = mid
			}
		}
		cut := lo
		if cut == 0 {
			cut = alignUp(s, 1) // one rune wider than the budget: emit whole
		} else if idx := strings.LastIndexByte(s[:cut], ' '); idx > 0 && cut < len(s) {
			cut = idx + 1 // prefer a word boundary
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}

// alignDown moves i back to the nearest rune boundary at or before i.
func alignDown(s string, i int) int {
	if i > len(s) {
		i = len(s)
	}
	for i > 0 && i < len(s) && s[i]&0xC0 == 0x80 {
		i--
	}
	return i
}

// alignUp moves i forward to the nearest rune boundary at or after i.
func alignUp(s string, i int) int {
	if i > len(s) {
		return len(s)
	}
	for i < len(s) && s[i]&0xC0 == 0x80 {
		i++
	}
	return i
}

// injectionCues are cheap lexical markers of instruction-manipulation
// phrasing. They are a prioritization heuristic only: they never create a
// finding, and the model is what decides.
var injectionCues = []string{
	"ignore previous", "ignore all previous", "disregard previous",
	"disregard all", "ignore the above", "forget your instructions",
	"new instructions", "system prompt", "your instructions",
	"override", "you must now", "do not tell", "do not reveal",
	"reveal the", "print the", "act as", "pretend you are",
	"developer mode", "jailbreak", "without restrictions",
}

// encodedRunChars is the alphabet a base64-ish or hex run draws from.
const encodedRunChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/-_="

// minSuspiciousRun is how long an unbroken encoded-looking run must be
// before it counts. Ordinary long words do not reach it.
const minSuspiciousRun = 32

// longestEncodedRun returns the length of the longest unbroken run of
// base64/hex alphabet characters in s.
func longestEncodedRun(s string) int {
	var best, cur int
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(encodedRunChars, s[i]) >= 0 {
			cur++
			if cur > best {
				best = cur
			}
			continue
		}
		cur = 0
	}
	if best < minSuspiciousRun {
		return 0
	}
	return best
}

// SuspicionScore is a cheap, deterministic priority score for one window.
//
// It decides which window gets a scarce inline slot, nothing else. A high
// score is not a finding and contributes no risk.
func SuspicionScore(text string) int {
	lower := strings.ToLower(text)
	var score int
	for _, cue := range injectionCues {
		if strings.Contains(lower, cue) {
			score += 2
		}
	}
	if longestEncodedRun(text) > 0 {
		score += 3
	}
	return score
}

// interleaveFromEnds returns the indices 0..n-1 ordered last, first,
// second-to-last, second, and so on.
//
// This is the tie-break order when no window looks more suspicious than
// another. Plain document order would mean that with max_inline_windows: 1
// only the first window is ever protected inline, and an attacker would
// simply put the payload at the end. Starting from the end costs nothing and
// removes that free placement.
func interleaveFromEnds(n int) []int {
	if n <= 0 {
		return nil
	}
	out := make([]int, 0, n)
	lo, hi := 0, n-1
	for lo <= hi {
		out = append(out, hi)
		if lo == hi {
			break
		}
		out = append(out, lo)
		lo++
		hi--
	}
	return out
}

// OrderWindows returns a new slice ordered by scoring priority: higher
// suspicion first, then the interleave-from-ends tie-break. Derived
// representations get a small bump, because content that only became
// readable after a decode is more interesting than content that was plain
// all along.
//
// The argument is not modified.
func OrderWindows(ws []WindowedText) []WindowedText {
	n := len(ws)
	if n <= 1 {
		return append([]WindowedText(nil), ws...)
	}

	type ranked struct {
		w        WindowedText
		score    int
		tieOrder int
	}
	tie := make([]int, n)
	for pos, idx := range interleaveFromEnds(n) {
		tie[idx] = pos
	}

	items := make([]ranked, n)
	for i, w := range ws {
		score := SuspicionScore(w.Text)
		if w.Window.Transform != "" {
			score++
		}
		items[i] = ranked{w: w, score: score, tieOrder: tie[i]}
	}
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].score != items[b].score {
			return items[a].score > items[b].score
		}
		return items[a].tieOrder < items[b].tieOrder
	})

	out := make([]WindowedText, 0, n)
	for _, it := range items {
		out = append(out, it.w)
	}
	return out
}
