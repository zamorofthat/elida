package preprocess

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// DecodeUnicodeEscapes resolves \xHH, \uHHHH and \UHHHHHHHH escapes.
//
// It is written by hand rather than with strconv.Unquote because content is
// not a Go string literal: Unquote requires surrounding quotes and rejects
// anything with an unescaped quote or newline, which is most real content.
//
// A malformed escape is left literal rather than failing the whole decode,
// because content routinely mixes escapes with Windows paths and LaTeX. A
// doubled backslash is consumed as an escaped backslash, so `a\\u0069b`
// is not treated as an escape. Lone surrogates are left literal: they do not
// encode a character and would produce invalid UTF-8.
func DecodeUnicodeEscapes(s string) (string, []string, bool) {
	if s == "" || !strings.Contains(s, `\`) {
		return s, nil, false
	}

	var b strings.Builder
	b.Grow(len(s))
	var decoded bool

	for i := 0; i < len(s); {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}

		switch s[i+1] {
		case '\\':
			// An escaped backslash: emit both bytes and move past them so
			// the following "u0069" is plain text, not an escape.
			b.WriteString(`\\`)
			i += 2
			continue
		case 'x':
			if r, n, ok := readHexEscape(s[i+2:], 2); ok {
				b.WriteRune(r)
				i += 2 + n
				decoded = true
				continue
			}
		case 'u':
			if r, n, ok := readHexEscape(s[i+2:], 4); ok {
				if utf16.IsSurrogate(r) {
					// Try to pair it with a following \uHHHH low surrogate.
					rest := s[i+2+n:]
					if strings.HasPrefix(rest, `\u`) {
						if r2, n2, ok2 := readHexEscape(rest[2:], 4); ok2 {
							if combined := utf16.DecodeRune(r, r2); combined != utf8.RuneError {
								b.WriteRune(combined)
								i += 2 + n + 2 + n2
								decoded = true
								continue
							}
						}
					}
					// Lone surrogate: leave it literal.
					break
				}
				b.WriteRune(r)
				i += 2 + n
				decoded = true
				continue
			}
		case 'U':
			if r, n, ok := readHexEscape(s[i+2:], 8); ok && !utf16.IsSurrogate(r) && r <= utf8.MaxRune {
				b.WriteRune(r)
				i += 2 + n
				decoded = true
				continue
			}
		}

		// Not a recognized escape: emit the backslash literally.
		b.WriteByte(s[i])
		i++
	}

	if !decoded {
		return s, nil, false
	}
	out := b.String()
	if out == s || !utf8.ValidString(out) {
		return s, nil, false
	}
	return out, []string{SignalEncodedPayload}, true
}

// readHexEscape parses exactly width hex digits from the front of s and
// returns the rune they encode, how many bytes were consumed, and whether
// the parse succeeded.
//
// The parsed value is checked against utf8.MaxRune before it is cast to
// rune. v is parsed with a 32-bit bit size, so it can be as large as
// 0xFFFFFFFF; casting a value of 0x80000000 or more straight to rune (a
// signed int32) wraps it negative, and a negative rune satisfies a naive
// "r <= utf8.MaxRune" check at the call site. Rejecting the out-of-range
// value here, before the cast, closes that hole for every caller.
func readHexEscape(s string, width int) (rune, int, bool) {
	if len(s) < width {
		return 0, 0, false
	}
	v, err := strconv.ParseUint(s[:width], 16, 32)
	if err != nil {
		return 0, 0, false
	}
	if v > utf8.MaxRune {
		return 0, 0, false
	}
	return rune(v), width, true
}
