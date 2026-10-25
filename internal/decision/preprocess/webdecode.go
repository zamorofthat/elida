package preprocess

import (
	"html"
	"net/url"
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

// DecodeHTMLEntities decodes HTML character references when the content
// actually contains one, using the same text-content decoding rules a
// browser applies.
//
// html.UnescapeString implements HTML5's "named character reference" state,
// which includes a fixed legacy set of references that resolve without a
// trailing semicolon ("&lt", "&gt", "&amp", "&quot", "&nbsp", "&not", and
// others), matched by maximal munch: the decoder consumes the longest
// recognized name it can, not necessarily the one a human skimming the
// source would guess. This is deliberate, not a looser approximation of
// HTML. An attacker crafting an injection payload relies on exactly this
// browser behavior to render as plain text; decoding any more
// conservatively than a browser does would leave that evasion undetected.
// The consequence is visible in "&notarealentity;": no entity named
// "notarealentity" exists, but a browser (and this function) still decodes
// the recognized "&not" prefix, producing "¬arealentity;" rather than
// leaving the text untouched — "¬" is correct per spec, not a bug.
//
// An "&" with no recognized reference after it is left alone:
// html.UnescapeString is lenient and returns such text unchanged, so the
// equality check below is what distinguishes "cats & dogs" from a real
// reference.
func DecodeHTMLEntities(s string) (string, []string, bool) {
	if s == "" || !strings.Contains(s, "&") {
		return s, nil, false
	}
	out := html.UnescapeString(s)
	if out == s {
		return s, nil, false
	}
	if !utf8.ValidString(out) {
		return s, nil, false
	}
	return out, []string{SignalEncodedPayload}, true
}
