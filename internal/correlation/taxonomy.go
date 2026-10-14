package correlation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Taxonomy bounds. ValidateTaxonomy enforces them; raising any of them
// weakens a bound and is a reserved decision.
//
// Per-call cost: Categorize lowercases the tool name once, then for each of
// at most MaxToolCategoryRules entries tests at most MaxToolGlobsPerRule
// globs (each at most MaxToolGlobBytes) against the name and, only if a glob
// matched, at most MaxArgumentPatternsPerRule regexes against at most
// MaxArgumentBytes of arguments. Go's regexp is RE2: matching is linear in
// the input with no backtracking, so no pattern is catastrophic, and the
// worst case is MaxToolCategoryRules x MaxArgumentPatternsPerRule linear
// scans of MaxArgumentBytes (128 x 32 x 64 KiB). Real taxonomies are a
// handful of entries, and an entry whose globs miss scans no arguments.
const (
	MaxToolCategoryRules       = 128
	MaxToolGlobsPerRule        = 32
	MaxArgumentPatternsPerRule = 32
	// MaxToolGlobBytes and MaxArgumentPatternBytes cap the length of one
	// glob and one argument pattern source, which bounds compiled size.
	MaxToolGlobBytes        = 256
	MaxArgumentPatternBytes = 1024
	// MaxArgumentBytes is how much of a tool call's arguments Categorize
	// scans: the first 64 KiB. A pattern that only matches past it is
	// missed; that is a documented limitation, not a bug.
	MaxArgumentBytes = 64 << 10
)

// ToolCategoryRule maps tool calls to a category. A call matches when its
// name matches any Tools glob and, if ArgumentPatterns is not empty, its
// arguments match any pattern.
//
// Tools are path.Match globs ('*', '?', '[...]', '\' escapes; '*' and '?'
// do not match '/'), matched case-insensitively: glob and name are both
// lowercased, because agent frameworks disagree on case ("Bash" vs "bash").
// path.Match, not filepath.Match, so the syntax is the same on every
// platform. ArgumentPatterns are RE2 regexes compiled with "(?i)", the
// convention of the tool_argument_pattern policy rules.
type ToolCategoryRule struct {
	Category         string   `yaml:"category" json:"category"`
	Tools            []string `yaml:"tools" json:"tools"`
	ArgumentPatterns []string `yaml:"argument_patterns,omitempty" json:"argument_patterns,omitempty"`
}

type compiledToolCategory struct {
	category string
	globs    []string
	patterns []*regexp.Regexp
}

// Taxonomy is a compiled, immutable tool-call category mapping, safe for
// concurrent use. It holds only configuration: it never retains a tool
// name or arguments it was asked about.
type Taxonomy struct {
	rules      []compiledToolCategory
	categories []string
}

// ValidateTaxonomy checks entries against the bounds above and compiles
// every glob and pattern. It returns every problem, not just the first.
// Fields are relative to the correlation block, e.g.
// "tool_categories[0].tools[1]".
func ValidateTaxonomy(rules []ToolCategoryRule) []FieldError {
	var errs []FieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if len(rules) > MaxToolCategoryRules {
		add("tool_categories", "at most %d entries, got %d", MaxToolCategoryRules, len(rules))
	}
	for i, r := range rules {
		p := fmt.Sprintf("tool_categories[%d]", i)
		if !ValidCategory(r.Category) {
			add(p+".category", "must be 1-64 lowercase letters, digits or underscores and not %q, got %q", CategoryMatch, r.Category)
		}
		switch {
		case len(r.Tools) == 0:
			add(p+".tools", "at least one tool name glob is required")
		case len(r.Tools) > MaxToolGlobsPerRule:
			add(p+".tools", "at most %d globs, got %d", MaxToolGlobsPerRule, len(r.Tools))
		default:
			for j, g := range r.Tools {
				f := fmt.Sprintf("%s.tools[%d]", p, j)
				switch {
				case g == "":
					add(f, "empty glob")
				case len(g) > MaxToolGlobBytes:
					add(f, "at most %d bytes, got %d", MaxToolGlobBytes, len(g))
				default:
					// path.Match reports a malformed pattern whatever the
					// name, so matching against "" validates it.
					if _, err := path.Match(strings.ToLower(g), ""); err != nil {
						add(f, "invalid glob %q: %v", g, err)
					}
				}
			}
		}
		if len(r.ArgumentPatterns) > MaxArgumentPatternsPerRule {
			add(p+".argument_patterns", "at most %d patterns, got %d", MaxArgumentPatternsPerRule, len(r.ArgumentPatterns))
			continue
		}
		for j, pat := range r.ArgumentPatterns {
			f := fmt.Sprintf("%s.argument_patterns[%d]", p, j)
			switch {
			case pat == "":
				add(f, "empty pattern; omit argument_patterns to match any arguments")
			case len(pat) > MaxArgumentPatternBytes:
				add(f, "at most %d bytes, got %d", MaxArgumentPatternBytes, len(pat))
			default:
				if _, err := regexp.Compile("(?i)" + pat); err != nil {
					add(f, "invalid pattern: %v", err)
				}
			}
		}
	}
	return errs
}

// NewTaxonomy validates and compiles rules once. No rules yields (nil, nil):
// a nil *Taxonomy categorizes nothing.
func NewTaxonomy(rules []ToolCategoryRule) (*Taxonomy, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	if errs := ValidateTaxonomy(rules); len(errs) > 0 {
		joined := make([]error, len(errs))
		for i := range errs {
			joined[i] = errs[i]
		}
		return nil, fmt.Errorf("invalid tool categories: %w", errors.Join(joined...))
	}
	t := &Taxonomy{}
	seen := make(map[string]bool)
	for _, r := range rules {
		c := compiledToolCategory{category: r.Category}
		for _, g := range r.Tools {
			c.globs = append(c.globs, strings.ToLower(g))
		}
		for _, pat := range r.ArgumentPatterns {
			c.patterns = append(c.patterns, regexp.MustCompile("(?i)"+pat))
		}
		t.rules = append(t.rules, c)
		if !seen[r.Category] {
			seen[r.Category] = true
			t.categories = append(t.categories, r.Category)
		}
	}
	return t, nil
}

// Categorize returns the categories a tool call falls into, or nil. Every
// entry is evaluated, so a call can fall into several categories; they come
// back distinct, in the configuration order of the first entry that
// produced each. The result depends only on the configuration and the
// inputs. Only the first MaxArgumentBytes of arguments are scanned. Neither
// the name nor the arguments are retained, logged or returned.
func (t *Taxonomy) Categorize(toolName, arguments string) []string {
	if t == nil || toolName == "" {
		return nil
	}
	name := strings.ToLower(toolName)
	if len(arguments) > MaxArgumentBytes {
		// A cut inside a multi-byte rune leaves invalid UTF-8 at the end,
		// which RE2 reads as U+FFFD; only that last partial rune is affected.
		arguments = arguments[:MaxArgumentBytes]
	}
	var out []string
	for _, r := range t.rules {
		if containsString(out, r.category) {
			continue // already produced: skip the scan
		}
		if !matchesAnyGlob(r.globs, name) {
			continue
		}
		if len(r.patterns) > 0 && !matchesAnyPattern(r.patterns, arguments) {
			continue
		}
		out = append(out, r.category)
	}
	return out
}

// Categories returns a copy of the distinct configured categories in
// configuration order, or nil.
func (t *Taxonomy) Categories() []string {
	if t == nil {
		return nil
	}
	return append([]string(nil), t.categories...)
}

func matchesAnyGlob(globs []string, name string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	return false
}

func matchesAnyPattern(patterns []*regexp.Regexp, s string) bool {
	for _, re := range patterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// toolEventVersion opens every canonical tool event string and names the
// hash domain, so an ID derived under a later form can never equal one
// derived under this one.
const toolEventVersion = "v1"

// ToolEventID is the stable ID of one category of one tool call: "te_"
// followed by the first 16 bytes of the SHA-256 of a canonical form,
// hex-encoded.
//
// With a provider call ID the identity is (session, call ID, category), so
// the same call reported twice, at any time or batch position, is one
// event. Without one it is (session, tool name, category, time in UTC with
// nanoseconds, ordinal within the batch). The first canonical field ("call"
// or "anon") keeps the two forms apart. Every field is length-prefixed
// ("|<len>:<value>") after the version, the convention of MatchID and
// internal/decision identity v3. The material is identifiers only, never
// arguments.
func ToolEventID(sessionID, callID, toolName, category string, at time.Time, ordinal int) string {
	var fields []string
	if callID != "" {
		fields = []string{"call", sessionID, callID, category}
	} else {
		fields = []string{"anon", sessionID, toolName, category, at.UTC().Format(time.RFC3339Nano), strconv.Itoa(ordinal)}
	}
	var b strings.Builder
	b.WriteString(toolEventVersion)
	for _, f := range fields {
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	sum := sha256.Sum256([]byte("elida.toolevent." + toolEventVersion + "\x00" + b.String()))
	return "te_" + hex.EncodeToString(sum[:16])
}
