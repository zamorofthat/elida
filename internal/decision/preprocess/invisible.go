package preprocess

import (
	"strings"
	"unicode/utf8"
)

// isZeroWidth reports whether r is a zero-width or otherwise
// non-rendering formatting character that can split a word without
// changing how a human or a model reads it.
//
// Ordinary whitespace is deliberately excluded: removing spaces, tabs and
// newlines would change the text a classifier sees in ways that are not
// evasion.
func isZeroWidth(r rune) bool {
	switch r {
	case '\u00ad', // SOFT HYPHEN
		'\u200b', // ZERO WIDTH SPACE
		'\u200c', // ZERO WIDTH NON-JOINER
		'\u200d', // ZERO WIDTH JOINER
		'\u2060', // WORD JOINER
		'\u2061', // FUNCTION APPLICATION
		'\u2062', // INVISIBLE TIMES
		'\u2063', // INVISIBLE SEPARATOR
		'\u2064', // INVISIBLE PLUS
		'\ufeff', // ZERO WIDTH NO-BREAK SPACE / BOM
		'\u180e': // MONGOLIAN VOWEL SEPARATOR
		return true
	}
	// Variation selectors: invisible, and attachable to any base character.
	if r >= '\ufe00' && r <= '\ufe0f' {
		return true
	}
	// Tag characters: an entire invisible ASCII alphabet.
	if r >= '\U000e0000' && r <= '\U000e007f' {
		return true
	}
	return false
}

// isBidiControl reports whether r is a bidirectional formatting control.
// These reorder rendered text without changing the code-point sequence, so
// what a reviewer sees and what the model reads can differ.
func isBidiControl(r rune) bool {
	switch r {
	case '\u200e', // LEFT-TO-RIGHT MARK
		'\u200f', // RIGHT-TO-LEFT MARK
		'\u202a', // LEFT-TO-RIGHT EMBEDDING
		'\u202b', // RIGHT-TO-LEFT EMBEDDING
		'\u202c', // POP DIRECTIONAL FORMATTING
		'\u202d', // LEFT-TO-RIGHT OVERRIDE
		'\u202e', // RIGHT-TO-LEFT OVERRIDE
		'\u2066', // LEFT-TO-RIGHT ISOLATE
		'\u2067', // RIGHT-TO-LEFT ISOLATE
		'\u2068', // FIRST STRONG ISOLATE
		'\u2069': // POP DIRECTIONAL ISOLATE
		return true
	}
	return false
}

// StripInvisible removes zero-width and bidirectional-control characters and
// reports which classes were present.
//
// Non-breaking space is intentionally left untouched here: NFKC already
// folds it to a plain space earlier in the pipeline, so treating it as
// zero-width in this transform too would be redundant.
//
// Invalid UTF-8 in s is preserved byte-for-byte rather than replaced with
// U+FFFD: decoding s to scan it must not corrupt bytes this transform does
// not otherwise touch, and the untouched fast path below already returns s
// unchanged, so the changed path has to match that behavior exactly.
//
// The signals matter as much as the output: the mere presence of these
// characters in user content or a tool result is grounds for admitting the
// message to the inline fast lane, whatever the stripped text then scores.
func StripInvisible(s string) (string, []string, bool) {
	if s == "" {
		return "", nil, false
	}

	var sawZeroWidth, sawBidi bool
	for _, r := range s {
		if isZeroWidth(r) {
			sawZeroWidth = true
		} else if isBidiControl(r) {
			sawBidi = true
		}
	}
	if !sawZeroWidth && !sawBidi {
		return s, nil, false
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid byte: not decodable, so it cannot be one of the runes
			// we strip. Keep it exactly as it was.
			b.WriteByte(s[i])
			i++
			continue
		}
		if !isZeroWidth(r) && !isBidiControl(r) {
			b.WriteRune(r)
		}
		i += size
	}

	// Signal order is fixed so tests and stored evidence are deterministic.
	var signals []string
	if sawZeroWidth {
		signals = append(signals, SignalZeroWidthRemoved)
	}
	if sawBidi {
		signals = append(signals, SignalBidiRemoved)
	}
	out := b.String()
	return out, signals, out != s
}
