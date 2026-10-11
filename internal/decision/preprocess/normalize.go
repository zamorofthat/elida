package preprocess

import "golang.org/x/text/unicode/norm"

// NormalizeNFKC applies Unicode NFKC normalization and reports whether
// anything changed.
//
// NFKC folds compatibility characters onto their canonical equivalents:
// fullwidth Latin, ligatures, superscripts, enclosed and Roman-numeral
// forms, and non-breaking spaces. Those forms are how an injection evades a
// regex that only knows ASCII.
//
// Returning changed=false when the text is already normalized keeps the
// pipeline from spending a representation slot on an identical string.
func NormalizeNFKC(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	if norm.NFKC.IsNormalString(s) {
		return s, false
	}
	out := norm.NFKC.String(s)
	return out, out != s
}
