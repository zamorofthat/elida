package preprocess

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// DecodeURL percent-decodes content when it actually contains valid percent
// escapes.
//
// url.PathUnescape is used rather than QueryUnescape because content is not
// a query string: turning "+" into a space in ordinary prose would corrupt
// text rather than reveal it. A malformed escape makes the whole decode
// fail, which is correct: "100%discount" is prose, not an encoded payload.
// Output that is not valid UTF-8 is rejected, because a classifier cannot
// read it and arbitrary bytes are not text.
func DecodeURL(s string) (string, []string, bool) {
	if s == "" || !strings.Contains(s, "%") {
		return s, nil, false
	}
	out, err := url.PathUnescape(s)
	if err != nil || out == s {
		return s, nil, false
	}
	if !utf8.ValidString(out) {
		return s, nil, false
	}
	return out, []string{SignalEncodedPayload}, true
}

// htmlEntityPattern matches one self-contained HTML character reference,
// numeric or named, always requiring the terminating semicolon.
//
// Requiring the semicolon on every candidate is what makes per-candidate
// validation in DecodeHTMLEntities possible: it guarantees the span this
// pattern captures is exactly the span html.UnescapeString itself would scan
// starting from the same "&", so decoding that captured span in isolation
// and checking the result (see DecodeHTMLEntities) reliably tells apart a
// real reference from one that only looks like a prefix of one.
var htmlEntityPattern = regexp.MustCompile(`&(?:#[0-9]+;|#[xX][0-9A-Fa-f]+;|[A-Za-z][A-Za-z0-9]*;)`)

// DecodeHTMLEntities decodes named and numeric HTML character references
// when the content actually contains at least one well-formed, unambiguous
// reference.
//
// Each candidate reference is decoded on its own, in isolation, rather than
// unescaping the whole string in one html.UnescapeString call. That matters
// because the standard library's decoder also recognizes a fixed legacy set
// of named references without a trailing semicolon (for example "not", the
// NOT SIGN, U+00AC): given "&notarealentity;", a whole-string unescape
// silently decodes just the "&not" prefix and leaves "arealentity;" behind
// as corrupted literal text, even though no real entity named
// "notarealentity" exists. A genuine reference, decoded on its own, always
// resolves to at most two runes (the standard library's own two-rune
// entities are the longest case); anything that decodes to more than that
// is the legacy-prefix case and is left untouched instead.
func DecodeHTMLEntities(s string) (string, []string, bool) {
	if s == "" || !strings.Contains(s, "&") {
		return s, nil, false
	}

	matches := htmlEntityPattern.FindAllStringIndex(s, -1)
	if matches == nil {
		return s, nil, false
	}

	var b strings.Builder
	b.Grow(len(s))
	changed := false
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		b.WriteString(s[last:start])
		last = end

		candidate := s[start:end]
		decoded := html.UnescapeString(candidate)
		if decoded == candidate || utf8.RuneCountInString(decoded) > 2 {
			b.WriteString(candidate) // no real reference, or only a legacy-prefix match
			continue
		}
		b.WriteString(decoded)
		changed = true
	}
	b.WriteString(s[last:])

	if !changed {
		return s, nil, false
	}
	out := b.String()
	if !utf8.ValidString(out) {
		return s, nil, false
	}
	return out, []string{SignalEncodedPayload}, true
}
