package preprocess

import "strings"

// functionWords are high-frequency English words whose presence is strong
// evidence that a string is English prose. They are chosen so that their
// ROT13 images ("gur", "naq", "lbh", ...) are not themselves English, which
// is what makes the comparison in DecodeROT13 meaningful.
var functionWords = []string{
	"the", "and", "you", "that", "for", "with", "this", "from", "have",
	"not", "are", "all", "your", "but", "they", "will", "would", "about",
	"what", "when", "which", "there", "their", "please", "should", "must",
	"ignore", "previous", "instructions", "system", "prompt", "user",
	"assistant", "print", "output", "secret", "password", "reveal",
}

// minROT13Bytes is the shortest text worth a lexical judgement. Below this,
// one coincidental function word dominates the score.
const minROT13Bytes = 24

// LexicalScore counts whole-word occurrences of high-frequency English
// words in s, case-insensitively.
//
// It is a crude signal and is used only comparatively: DecodeROT13 asks
// whether the rotated text looks more English than the input, never whether
// either is "English enough" on an absolute scale.
func LexicalScore(s string) int {
	if s == "" {
		return 0
	}
	lower := strings.ToLower(s)
	fields := strings.FieldsFunc(lower, func(r rune) bool {
		return r < 'a' || r > 'z'
	})
	index := make(map[string]bool, len(functionWords))
	for _, w := range functionWords {
		index[w] = true
	}
	var score int
	for _, f := range fields {
		if index[f] {
			score++
		}
	}
	return score
}

// rot13 rotates ASCII letters by 13 and leaves everything else alone.
func rot13(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = 'a' + (c-'a'+13)%26
		case c >= 'A' && c <= 'Z':
			b[i] = 'A' + (c-'A'+13)%26
		}
	}
	return string(b)
}

// DecodeROT13 rotates s by 13 and keeps the result only if it reads as
// English more convincingly than the input does.
//
// ROT13 is its own inverse, so an unconditional pass would produce a
// representation for every string and a second one that equals the original.
// The lexical gate means English input produces nothing (its rotation is
// gibberish) and rotated input produces one representation.
func DecodeROT13(s string, b Budget) (string, []string, bool) {
	if len(s) < minROT13Bytes {
		return s, nil, false
	}
	out := rot13(s)
	if out == s {
		return s, nil, false // no ASCII letters at all
	}
	inScore := LexicalScore(s)
	outScore := LexicalScore(out)
	// Require a clear win, not a tie: two function words of margin keeps a
	// single coincidence from flipping the decision.
	if outScore < 2 || outScore <= inScore+1 {
		return s, nil, false
	}
	if b.MaxExpansionRatio > 0 && len(out) > len(s)*b.MaxExpansionRatio {
		return s, nil, false
	}
	return out, []string{SignalEncodedPayload}, true
}
