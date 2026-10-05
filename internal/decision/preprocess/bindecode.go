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

// longestRun returns the longest substring of s made only of characters in
// allowed.
func longestRun(s, allowed string) string {
	var bestStart, bestLen, start, length int
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(allowed, s[i]) >= 0 {
			if length == 0 {
				start = i
			}
			length++
			if length > bestLen {
				bestLen, bestStart = length, start
			}
			continue
		}
		length = 0
	}
	if bestLen == 0 {
		return ""
	}
	return s[bestStart : bestStart+bestLen]
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

// DecodeBase64 finds the longest base64-looking run in s, decodes it, and
// keeps the result only if it reads as text.
//
// The gate is deliberately strict: plausible alphabet, at least
// MinEncodedRunBytes long, decodes cleanly under some base64 variant, at
// least minPrintableRatio printable, valid UTF-8, and within the caller's
// expansion ratio. Decoding English prose into binary and then scoring that
// binary would be worse than not decoding at all.
func DecodeBase64(s string, b Budget) (string, []string, bool) {
	if len(s) < MinEncodedRunBytes {
		return s, nil, false
	}
	run := longestRun(s, base64RunChars)
	if len(run) < MinEncodedRunBytes {
		return s, nil, false
	}
	raw, ok := decodeBase64Run(run)
	if !ok {
		return s, nil, false
	}
	out := string(raw)
	if !utf8.ValidString(out) || PrintableRatio(out) < minPrintableRatio {
		return s, nil, false
	}
	if out == s || strings.Contains(s, out) {
		// Decoding produced something already present: no new information.
		return s, nil, false
	}
	if b.MaxExpansionRatio > 0 && len(out) > len(run)*b.MaxExpansionRatio {
		return s, nil, false
	}
	return out, []string{SignalEncodedPayload}, true
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

// DecodeHex finds the longest hexadecimal run in s, decodes it, and keeps
// the result only if it reads as text.
//
// The same gate as DecodeBase64 applies, plus an even-length requirement. A
// leading "0x" is stripped before the run is measured.
func DecodeHex(s string, b Budget) (string, []string, bool) {
	if len(s) < MinEncodedRunBytes {
		return s, nil, false
	}
	candidate := s
	if idx := indexHexPrefix(candidate); idx >= 0 {
		candidate = candidate[idx+2:]
	}
	run := longestRun(candidate, hexRunChars)
	if len(run) < MinEncodedRunBytes || len(run)%2 != 0 {
		return s, nil, false
	}
	raw, err := hex.DecodeString(run)
	if err != nil || len(raw) == 0 {
		return s, nil, false
	}
	out := string(raw)
	if !utf8.ValidString(out) || PrintableRatio(out) < minPrintableRatio {
		return s, nil, false
	}
	if out == s || strings.Contains(s, out) {
		return s, nil, false
	}
	if b.MaxExpansionRatio > 0 && len(out) > len(run)*b.MaxExpansionRatio {
		return s, nil, false
	}
	return out, []string{SignalEncodedPayload}, true
}
