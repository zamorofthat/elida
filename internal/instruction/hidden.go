package instruction

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// HiddenContent says which invisible code points a hidden-content rule
// found. Labels, counts and positions only.
type HiddenContent struct {
	CodePoints map[string]int `json:"code_points"` // "U+202E" -> occurrences
	Lines      []int          `json:"lines"`       // 1-based, ascending, distinct, at most MaxHiddenLines
	Total      int            `json:"total"`
}

// MaxHiddenLines bounds how many line numbers a HiddenContent lists.
const MaxHiddenLines = 20

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

// tallyHidden counts every rune matched by any pattern, once per position.
// A U+FEFF that is the first rune of content is a byte-order mark and is not
// counted. It returns nil when nothing was counted.
func tallyHidden(content string, patterns []*regexp.Regexp) *HiddenContent {
	seen := make(map[int]rune)
	for _, re := range patterns {
		for _, loc := range re.FindAllStringIndex(content, -1) {
			for i, r := range content[loc[0]:loc[1]] {
				seen[loc[0]+i] = r
			}
		}
	}
	h := &HiddenContent{CodePoints: map[string]int{}, Lines: []int{}}
	line := 1
	for i, r := range content {
		if r == '\n' {
			line++
		}
		if _, ok := seen[i]; !ok {
			continue
		}
		if i == 0 && r == 0xFEFF {
			continue
		}
		h.CodePoints[fmt.Sprintf("U+%04X", r)]++
		h.Total++
		l := line
		if r == '\n' {
			l-- // the newline itself belongs to the line it ends
		}
		if n := len(h.Lines); (n == 0 || h.Lines[n-1] != l) && n < MaxHiddenLines {
			h.Lines = append(h.Lines, l)
		}
	}
	if h.Total == 0 {
		return nil
	}
	return h
}
