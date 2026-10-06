package preprocess

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MinEncodedRunBytes is the shortest encoded run worth decoding. Below this,
// base64 and hex decoders are overwhelmingly more likely to find a false
// positive in ordinary prose than a real payload.
const MinEncodedRunBytes = 24

// minPrintableRatio is how much of a decoded result must be printable text
// for the result to be worth scoring. A classifier cannot read binary, and
// an image or an archive decoded out of a tool result is noise.
const minPrintableRatio = 0.9

// PrintableRatio returns the fraction of runes in s that are printable text
// (letters, digits, punctuation, symbols, spaces, tabs and newlines).
//
// Invalid UTF-8 counts against the ratio, so arbitrary bytes score near
// zero. An empty string scores 0, not 1: nothing is not text.
func PrintableRatio(s string) float64 {
	if s == "" {
		return 0
	}
	var total, printable int
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		total++
		i += size
		if r == utf8.RuneError && size == 1 {
			continue // invalid byte
		}
		switch {
		case unicode.IsPrint(r), r == '\n', r == '\r', r == '\t':
			printable++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(printable) / float64(total)
}

// base64RunChars are the characters that can appear in a base64 run,
// covering both the standard and URL-safe alphabets.
const base64RunChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/-_="

// maxCandidateRuns bounds how many encoded-looking runs one decode examines.
//
// Looking at only the single longest run is evadable: appending a run of
// filler characters longer than the real payload makes the decoy the only
// candidate, the decoy decodes to bytes the printable gate rejects, and the
// real payload is never examined — a bypass for the cost of a few bytes.
// Four candidates defeat a handful of stacked decoys while keeping the work
// per call constant.
const maxCandidateRuns = 4

// candidateRuns returns up to maxCandidateRuns substrings of s made only of
// characters in allowed, longest first, skipping any run shorter than
// MinEncodedRunBytes.
//
// It is one pass over s. Each completed run is offered to a fixed-size
// ranking, so the extra work per run is constant and the only allocation is
// one bounded slice of results.
func candidateRuns(s, allowed string) []string {
	type span struct{ start, length int }
	var best [maxCandidateRuns]span
	kept := 0

	consider := func(start, length int) {
		if length < MinEncodedRunBytes {
			return
		}
		// Where this run lands among the ones already kept.
		pos := kept
		for pos > 0 && best[pos-1].length < length {
			pos--
		}
		if pos >= maxCandidateRuns {
			return // shorter than every run kept, and the ranking is full
		}
		// Shift the weaker runs down, dropping the weakest if full.
		for i := min(kept, maxCandidateRuns-1); i > pos; i-- {
			best[i] = best[i-1]
		}
		best[pos] = span{start, length}
		if kept < maxCandidateRuns {
			kept++
		}
	}

	var start, length int
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(allowed, s[i]) >= 0 {
			if length == 0 {
				start = i
			}
			length++
			continue
		}
		consider(start, length)
		length = 0
	}
	consider(start, length)

	out := make([]string, 0, kept)
	for i := 0; i < kept; i++ {
		out = append(out, s[best[i].start:best[i].start+best[i].length])
	}
	return out
}

// decodeBase64Run tries every base64 variant against one candidate run.
func decodeBase64Run(run string) ([]byte, bool) {
	trimmed := strings.TrimRight(run, "=")
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.RawURLEncoding,
	} {
		if out, err := enc.DecodeString(run); err == nil && len(out) > 0 {
			return out, true
		}
		if out, err := enc.DecodeString(trimmed); err == nil && len(out) > 0 {
			return out, true
		}
	}
	return nil, false
}

// DecodeBase64 walks the longest base64-looking runs in s, longest first,
// and returns the first decode that reads as text.
//
// The gate is deliberately strict: plausible alphabet, at least
// MinEncodedRunBytes long, decodes cleanly under some base64 variant, at
// least minPrintableRatio printable, valid UTF-8, not already present in s,
// and within the caller's expansion ratio. Decoding English prose into
// binary and then scoring that binary would be worse than not decoding at
// all.
//
// Two bounds are worth knowing when reading a negative result:
//
//   - At most maxCandidateRuns runs are examined. Padding a message with
//     more than that many long decoy runs can still bury a real payload,
//     and no gap is recorded when that happens.
//   - Runs are maximal over the base64 alphabet, which is every ASCII
//     letter and digit. A payload glued directly to an adjacent word, with
//     no separator between them, merges into one run with that word and
//     will not decode. Whitespace, punctuation or a quote on either side is
//     enough to separate it.
func DecodeBase64(s string, b Budget) (string, []string, bool) {
	if len(s) < MinEncodedRunBytes {
		return s, nil, false
	}
	for _, run := range candidateRuns(s, base64RunChars) {
		raw, ok := decodeBase64Run(run)
		if !ok {
			continue
		}
		out := string(raw)
		if !utf8.ValidString(out) || PrintableRatio(out) < minPrintableRatio {
			continue
		}
		if out == s || strings.Contains(s, out) {
			continue // already present: no new information
		}
		if b.MaxExpansionRatio > 0 && len(out) > len(run)*b.MaxExpansionRatio {
			continue
		}
		return out, []string{SignalEncodedPayload}, true
	}
	return s, nil, false
}

const hexRunChars = "0123456789abcdefABCDEF"

// indexHexPrefix finds the first "0x"/"0X" marker in s, scanning byte by
// byte so the returned index is always an offset into s itself.
//
// strings.ToLower(s) is not used for this: case-folding some runes changes
// a string's byte length (an invalid UTF-8 byte is replaced by the 3-byte
// U+FFFD; some letters, like Turkish 'I-dot' or German 'ẞ', also change
// width), so an index found in a lowercased copy can land past the end of
// the original, or mid-rune. "0" has no case, so comparing it directly
// against s and only case-folding the single following byte is both
// correct and immune to that drift.
func indexHexPrefix(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') {
			return i
		}
	}
	return -1
}

// DecodeHex walks the longest hexadecimal runs in s, longest first, and
// returns the first decode that reads as text.
//
// The same gate and the same maxCandidateRuns bound as DecodeBase64 apply,
// plus an even-length requirement: an odd-length run is skipped and the next
// candidate is tried. A leading "0x" is stripped before the runs are
// measured.
func DecodeHex(s string, b Budget) (string, []string, bool) {
	if len(s) < MinEncodedRunBytes {
		return s, nil, false
	}
	candidate := s
	if idx := indexHexPrefix(candidate); idx >= 0 {
		candidate = candidate[idx+2:]
	}
	for _, run := range candidateRuns(candidate, hexRunChars) {
		if len(run)%2 != 0 {
			continue
		}
		raw, err := hex.DecodeString(run)
		if err != nil || len(raw) == 0 {
			continue
		}
		out := string(raw)
		if !utf8.ValidString(out) || PrintableRatio(out) < minPrintableRatio {
			continue
		}
		if out == s || strings.Contains(s, out) {
			continue
		}
		if b.MaxExpansionRatio > 0 && len(out) > len(run)*b.MaxExpansionRatio {
			continue
		}
		return out, []string{SignalEncodedPayload}, true
	}
	return s, nil, false
}
