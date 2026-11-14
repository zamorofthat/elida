package instruction

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// HiddenContent says which invisible code points a hidden-content rule
// found. Labels, counts and positions only.
type HiddenContent struct {
	CodePoints map[string]int `json:"code_points"` // "U+202E" -> occurrences; invisible code points only
	Lines      []int          `json:"lines"`       // 1-based, ascending, distinct, at most MaxHiddenLines
	Total      int            `json:"total"`       // every matched rune, visible or not, at most MaxHiddenCounted
	Truncated  bool           `json:"truncated"`   // counting stopped at MaxHiddenCounted: Total is a lower bound
}

// MaxHiddenLines bounds how many line numbers a HiddenContent lists.
const MaxHiddenLines = 20

// MaxHiddenCounted bounds how many matched runes a HiddenContent counts, so
// a file made of nothing but invisible runes costs a bounded tally.
const MaxHiddenCounted = 10000

// isInvisible reports whether r is a format character or in the tag block.
func isInvisible(r rune) bool {
	return unicode.Is(unicode.Cf, r) || (r >= 0xE0000 && r <= 0xE007F)
}

// stripInvisible drops every unicode.Cf rune and U+E0000-U+E007F.
func stripInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if isInvisible(r) {
			return -1
		}
		return r
	}, s)
}

// tallyHidden counts the runes matched by any pattern, once per position, up
// to MaxHiddenCounted (then Truncated is set). A U+FEFF that is the first
// rune of content is a byte-order mark and is not counted. Only runes that
// are invisible (isInvisible) are labelled in CodePoints and give a line
// number: a pattern that matches visible text has it counted in Total only.
// It returns nil when nothing was counted.
func tallyHidden(content string, patterns []*regexp.Regexp) *HiddenContent {
	seen := make(map[int]struct{})
	truncated := false
collect:
	for _, re := range patterns {
		for _, loc := range re.FindAllStringIndex(content, MaxHiddenCounted+1) {
			for i, r := range content[loc[0]:loc[1]] {
				pos := loc[0] + i
				if pos == 0 && r == 0xFEFF {
					continue
				}
				if _, dup := seen[pos]; dup {
					continue
				}
				if len(seen) == MaxHiddenCounted {
					truncated = true
					break collect
				}
				seen[pos] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}

	positions := make([]int, 0, len(seen))
	for pos := range seen {
		positions = append(positions, pos)
	}
	sort.Ints(positions)
	h := &HiddenContent{CodePoints: map[string]int{}, Lines: []int{}, Total: len(seen), Truncated: truncated}
	line, at := 1, 0
	for _, pos := range positions {
		line += strings.Count(content[at:pos], "\n")
		at = pos
		r, _ := utf8.DecodeRuneInString(content[pos:])
		if !isInvisible(r) {
			continue
		}
		h.CodePoints[fmt.Sprintf("U+%04X", r)]++
		if n := len(h.Lines); (n == 0 || h.Lines[n-1] != line) && n < MaxHiddenLines {
			h.Lines = append(h.Lines, line)
		}
	}
	return h
}
