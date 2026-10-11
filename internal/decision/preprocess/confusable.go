package preprocess

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// confusables maps non-ASCII code points to the ASCII character they are
// visually confusable with.
//
// This table is deliberately explicit and deliberately incomplete. The full
// Unicode confusables data is large and mostly irrelevant to prompt
// injection; what matters is the handful of scripts that supply
// pixel-identical substitutes for ASCII letters and digits. Extend it when a
// new substitution shows up in real traffic rather than importing the whole
// table.
//
// Every non-ASCII key is written as a \u/\U escape rather than a literal
// character: an editor or a tool round-trip can silently Unicode-normalize a
// literal homoglyph in source, collapsing it onto a different, visually
// similar code point and corrupting the table without a visible diff.
//
// Mathematical alphanumeric symbols are handled by NFKC upstream, but the
// skeleton runs on the original too, so the bold/italic Latin blocks are
// folded here by range rather than enumerated.
var confusables = map[rune]rune{
	// Cyrillic lowercase
	'\u0430': 'a', '\u0432': 'b', '\u0441': 'c', '\u0435': 'e', '\u0433': 'r',
	'\u043D': 'h', '\u0456': 'i', '\u0458': 'j', '\u043A': 'k', '\u043C': 'm',
	'\u043E': 'o', '\u0440': 'p', '\u0455': 's', '\u0442': 't', '\u0443': 'y',
	'\u0445': 'x', '\u0491': 'r', '\u04CF': 'l', '\u0501': 'd', '\u051B': 'q',
	'\u051D': 'w', '\u0475': 'v', '\u0261': 'g', '\u0578': 'n',
	// Cyrillic lowercase digit lookalike (mirrors the
	// uppercase entry below; see the task-9 report for why
	// this one entry was added to the brief's table)
	'\u0447': '4',
	// Cyrillic uppercase
	'\u0410': 'A', '\u0412': 'B', '\u0421': 'C', '\u0415': 'E', '\u041D': 'H',
	'\u041A': 'K', '\u041C': 'M', '\u041E': 'O', '\u0420': 'P', '\u0405': 'S',
	'\u0422': 'T', '\u0423': 'Y', '\u0425': 'X', '\u0406': 'I', '\u0408': 'J',
	'\u0417': '3', '\u0427': '4', '\u0411': '6',
	// Greek lowercase
	'\u03B1': 'a', '\u03B2': 'b', '\u03B3': 'y', '\u03B5': 'e', '\u03B9': 'i',
	'\u03BA': 'k', '\u03BD': 'v', '\u03BF': 'o', '\u03C1': 'p', '\u03C3': 'o',
	'\u03C4': 't', '\u03C5': 'u', '\u03C7': 'x', '\u03BC': 'u',
	// Greek uppercase
	'\u0391': 'A', '\u0392': 'B', '\u0395': 'E', '\u0396': 'Z', '\u0397': 'H',
	'\u0399': 'I', '\u039A': 'K', '\u039C': 'M', '\u039D': 'N', '\u039F': 'O',
	'\u03A1': 'P', '\u03A4': 'T', '\u03A5': 'Y', '\u03A7': 'X',
	// Armenian
	'\u0570': 'h', '\u0585': 'o', '\u057C': 'n', '\u0581': 'g',
	// Cherokee
	'\u13A0': 'D', '\u13A1': 'R', '\u13A2': 'T', '\u13AA': 'G', '\u13B3': 'W',
	'\u13C0': 'G', '\u13C2': 'h', '\u13CE': 'S', '\u13CF': 'b', '\u13D2': 'P',
	// Letterlike and other singletons
	'\u2113': 'l', '\u217C': 'l', '\u01C0': 'l', '\u0131': 'i', '\u0237': 'j',
	'\u0183': 'b', '\u01BF': 'p', '\u0280': 'R',
	// Digit lookalikes
	'\u06F0': '0', '\u07C0': '0', '\u2070': '0', '\uFF10': '0', '\u0661': '1',
	'\u06F1': '1', '\u2081': '1', '\u0662': '2', '\u01A7': '2', '\u0663': '3',
	'\u01B7': '3', '\u0664': '4', '\u0665': '5', '\u01BD': '5', '\u0666': '6',
	'\u0667': '7', '\u0668': '8', '\u0669': '9',
}

// foldLatinRange folds the Mathematical Alphanumeric Symbols and Fullwidth
// Latin blocks onto ASCII. These are contiguous, so a range check beats an
// entry per code point.
func foldLatinRange(r rune) (rune, bool) {
	switch {
	case r >= '\uFF21' && r <= '\uFF3A': // FULLWIDTH LATIN CAPITAL
		return 'A' + (r - '\uFF21'), true
	case r >= '\uFF41' && r <= '\uFF5A': // FULLWIDTH LATIN SMALL
		return 'a' + (r - '\uFF41'), true
	case r >= '\uFF10' && r <= '\uFF19': // FULLWIDTH DIGIT
		return '0' + (r - '\uFF10'), true
	case r >= '\U0001D400' && r <= '\U0001D7FF':
		// Mathematical alphanumerics: 26 uppercase, 26 lowercase per style
		// block, with holes for reserved code points that NFKD maps anyway.
		folded := norm.NFKD.String(string(r))
		for _, f := range folded {
			if f < unicode.MaxASCII {
				return f, true
			}
		}
	}
	return r, false
}

// latinDiacriticBase reports whether r is a single precomposed Latin letter
// carrying combining marks only -- e.g. "o" + combining circumflex, as
// decomposed from precomposed "ô" -- and if so returns the bare ASCII base.
//
// The check is deliberately per-rune rather than a whole-string NFKD pass:
// decomposing the entire input and reassembling it as base-plus-marks would
// also split apart real non-Latin characters that NFKD decomposes for
// unrelated reasons (a Japanese voiced-sound mark on a kana, an Arabic
// harakat, a Devanagari matra), changing their byte representation even when
// nothing about them is confusable. Testing one rune's own decomposition in
// isolation, and only accepting it when the base is ASCII and everything
// after it is a nonspacing mark (so a true multi-letter decomposition like
// "ﬁ" -> "f"+"i" is correctly rejected as not a diacritic), confines the
// fold to actual Latin-letter-plus-accent cases and leaves every other
// script's code points byte-identical to their input.
func latinDiacriticBase(r rune) (rune, bool) {
	dec := norm.NFKD.String(string(r))
	first, size := utf8.DecodeRuneInString(dec)
	if first == utf8.RuneError || first >= utf8.RuneSelf {
		return 0, false
	}
	for _, m := range dec[size:] {
		if !unicode.Is(unicode.Mn, m) {
			return 0, false
		}
	}
	return first, true
}

// ConfusableSkeleton folds visually confusable code points, Latin letters
// carrying diacritics, and stray combining marks left on an ASCII letter,
// onto ASCII, producing a "skeleton" that a classifier can read as plain
// text.
//
// Limitations: the table covers the Latin-lookalike code points from the
// scripts that supply them (Cyrillic, Greek, Armenian, Cherokee, fullwidth,
// mathematical alphanumerics) plus Arabic-Indic and fullwidth digits. It is
// not the full Unicode confusables set, and it intentionally leaves genuine
// non-Latin text alone: folding real Japanese, Chinese or Arabic to ASCII
// would produce noise, not evidence. See latinDiacriticBase for why
// diacritic folding is done rune-by-rune instead of as one NFKD pass over
// the whole string.
//
// A standalone nonspacing mark (Unicode category Mn) that was not already
// consumed by latinDiacriticBase -- because it arrived as its own rune
// rather than inside a single precomposed character -- is dropped when the
// most recently emitted rune was ASCII, and kept otherwise. This closes an
// evasion path the precomposed-only check leaves open: a mark like U+0334
// (COMBINING TILDE OVERLAY) has no precomposed form to fold via
// latinDiacriticBase, so "ign" + U+0334 + "ore" would otherwise survive
// unmodified even though it reads as "ignore" with an invisible rider. The
// rule is keyed off the emitted rune, not the original input rune, so a
// chain of several marks after one ASCII letter is handled one at a time
// correctly. It is also keyed off ASCII specifically, not "folded by this
// function": a mark stacked on a base this function legitimately leaves
// alone -- Hebrew niqqud on a Hebrew letter, a Devanagari nukta or virama on
// a Devanagari letter, Arabic harakat on an Arabic letter -- sees a
// non-ASCII emitted rune immediately before it and is kept, because that
// base was never ASCII to begin with.
//
// Invalid UTF-8 in s is preserved byte-for-byte rather than replaced with
// U+FFFD: the scan walks s by byte index with utf8.DecodeRuneInString, and
// an undecodable byte is copied forward with WriteByte instead of being
// rewritten by WriteRune, matching the pattern StripInvisible uses for the
// same reason. Every rune this function does not specifically fold is
// copied through unchanged, so real text in any other script is
// byte-identical on the way out.
func ConfusableSkeleton(s string) (string, []string, bool) {
	if s == "" {
		return "", nil, false
	}

	var b strings.Builder
	b.Grow(len(s))
	lastEmittedASCII := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid byte: not decodable, so it cannot be a confusable
			// rune or a diacritic. Keep it exactly as it was.
			b.WriteByte(s[i])
			i++
			lastEmittedASCII = false
			continue
		}
		i += size

		if ascii, ok := confusables[r]; ok {
			b.WriteRune(ascii)
			lastEmittedASCII = true
			continue
		}
		if folded, ok := foldLatinRange(r); ok {
			b.WriteRune(folded)
			lastEmittedASCII = true
			continue
		}
		if r >= utf8.RuneSelf {
			if base, ok := latinDiacriticBase(r); ok {
				b.WriteRune(base)
				lastEmittedASCII = true
				continue
			}
			if unicode.Is(unicode.Mn, r) {
				if lastEmittedASCII {
					// Stray combining mark riding on an ASCII letter: no
					// precomposed form routed it through latinDiacriticBase,
					// so catch it here instead of letting it survive.
					continue
				}
				b.WriteRune(r) // mark on a non-Latin base: real text, keep it
				continue
			}
		}
		b.WriteRune(r)
		lastEmittedASCII = r < utf8.RuneSelf
	}

	out := b.String()
	if out == s {
		return s, nil, false
	}
	return out, []string{SignalConfusables}, true
}
