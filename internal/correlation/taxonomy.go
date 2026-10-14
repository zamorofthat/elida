package correlation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Taxonomy bounds. ValidateTaxonomy enforces them; raising any of them
// weakens a bound and is a reserved decision.
//
// Cost model. Go's regexp is RE2: it never backtracks exponentially, but a
// match costs time proportional to input length x compiled program size
// (the NFA simulation can keep a thread live per instruction per input
// byte), and a case-folded pattern ("(?i)", which every argument pattern
// gets) loses the literal-prefix fast path. A short pattern can compile to
// a large program ("[a-h]{1000}9" is 12 bytes and about 1000 instructions),
// so source length does not bound cost. The caps therefore bound compiled
// programs: every glob and every argument pattern is compiled at
// construction and rejected above MaxPatternInstructions, and the summed
// program length of the whole taxonomy, globs included, is rejected above
// MaxTaxonomyInstructions. Per call, Categorize runs glob programs over at
// most MaxToolNameBytes of name and argument programs over at most the
// configured max_argument_bytes of arguments, so the work is bounded by
// MaxTaxonomyInstructions x max_argument_bytes instruction-steps.
//
// Measured per call on an Apple M1 Pro (no -race), with the budget filled
// to the cap and arguments that never match:
//
//	shape (per-pattern instructions)        16 KiB default   64 KiB ceiling
//	"q0z[a-f]+\d" x512 (8, literal-led)      141 ms           749 ms
//	"(a|..|h)*[a-h]{20}x0" x284 (28, NFA)    1.36 s           5.42 s
//	"[a-h]{200}0" x40 (203, NFA)             1.52 s           6.16 s
//	realistic: 3 entries, 7 patterns         1.3 ms           10.4 ms
//
// (89 µs for the realistic taxonomy at 1 KiB.) The bound is real but
// coarse: operators own the patterns, and a pathological taxonomy can still
// add over a second per tool call at the default scan size. What an
// attacker controls, the argument bytes, is bounded by max_argument_bytes;
// the program size is operator-authored. A taxonomy over
// WarnTaxonomyInstructions is accepted with a warning (TaxonomyWarnings,
// Taxonomy.Warnings) that carries EstimatedWorstCaseCost.
//
// Callers must run Categorize off the request path (an async lane or the
// session-end path) and never under a proxy or policy lock.
const (
	// WarnTaxonomyInstructions is the soft limit on summed compiled
	// instructions: above it the taxonomy is accepted with a warning.
	WarnTaxonomyInstructions = 1024
	// MaxTaxonomyEntries caps tool_categories entries.
	MaxTaxonomyEntries = 64
	// MaxGlobsPerEntry caps the tools globs of one entry.
	MaxGlobsPerEntry = 32
	// MaxPatternsPerEntry caps the argument_patterns of one entry.
	MaxPatternsPerEntry = 8
	// MaxToolGlobBytes and MaxArgumentPatternBytes cap source length; the
	// instruction caps below are what bound cost.
	MaxToolGlobBytes        = 128
	MaxArgumentPatternBytes = 1024
	// MaxPatternInstructions caps the compiled program of one glob or one
	// argument pattern.
	MaxPatternInstructions = 256
	// MaxTaxonomyInstructions caps the summed compiled program length of
	// every glob and argument pattern in the taxonomy.
	MaxTaxonomyInstructions = 8192
	// MaxToolNameBytes is the longest tool name Categorize considers. A
	// longer name falls into no category; provider limits on tool names are
	// 64-128 bytes, so no real tool is affected.
	MaxToolNameBytes = 256
	// DefaultMaxArgumentBytes is the default max_argument_bytes: how much of
	// a tool call's arguments Categorize scans.
	DefaultMaxArgumentBytes = 16 << 10
	// MaxArgumentBytesCeiling is the largest max_argument_bytes accepted.
	MaxArgumentBytesCeiling = 64 << 10
)

// TaxonomyConfig is the taxonomy part of the correlation configuration
// block.
type TaxonomyConfig struct {
	ToolCategories []ToolCategoryRule `yaml:"tool_categories,omitempty" json:"tool_categories,omitempty"`
	// MaxArgumentBytes is how much of a tool call's arguments are scanned:
	// the first MaxArgumentBytes bytes. A pattern that only matches past it
	// is missed; that is a documented limitation. 0 means
	// DefaultMaxArgumentBytes; at most MaxArgumentBytesCeiling.
	MaxArgumentBytes int `yaml:"max_argument_bytes,omitempty" json:"max_argument_bytes,omitempty"`
}

// ToolCategoryRule maps tool calls to a category. A call matches when its
// name matches any Tools glob and, if ArgumentPatterns is not empty, its
// arguments match any pattern. Several entries may name the same category,
// and globs may overlap; a call still gets each category once.
//
// Tools are globs matched against the whole name, case-insensitively.
// Tool names are not paths: '*' matches any run of characters and '?' any
// one character, '/' included. '[...]' is a character class ('[^...]'
// negates, 'a-z' is a range) and '\' escapes the next character; every
// other character is literal. ArgumentPatterns are RE2 regexes compiled
// with "(?i)", the convention of the tool_argument_pattern policy rules.
type ToolCategoryRule struct {
	Category         string   `yaml:"category" json:"category"`
	Tools            []string `yaml:"tools" json:"tools"`
	ArgumentPatterns []string `yaml:"argument_patterns,omitempty" json:"argument_patterns,omitempty"`
}

type compiledToolCategory struct {
	category string
	globs    []*regexp.Regexp
	patterns []*regexp.Regexp
}

// Taxonomy is a compiled, immutable tool-call category mapping, safe for
// concurrent use. It holds only configuration: it never retains a tool
// name or arguments it was asked about.
type Taxonomy struct {
	rules      []compiledToolCategory
	categories []string
	maxArgs    int
	// instructions is the summed compiled program length, globs included.
	instructions int
}

// Cost estimate calibration: the worst case measured at a full
// MaxTaxonomyInstructions budget of NFA-heavy patterns over the default
// scan size on an Apple M1 Pro (see the table above).
const (
	worstCaseAtFullBudget = 1400 * time.Millisecond
	worstCaseBytes        = DefaultMaxArgumentBytes
)

// EstimatedWorstCaseCost estimates the worst-case Categorize time per tool
// call for a taxonomy of the given summed compiled instructions scanning
// maxArgumentBytes of arguments: roughly
// 1.4 s x (instructions / 8192) x (maxArgumentBytes / 16 KiB), the measured
// NFA-heavy worst case on an Apple M1 Pro scaled linearly. It is an order of
// magnitude for an operator warning, not a guarantee: literal-led patterns
// run far faster, and other machines differ.
func EstimatedWorstCaseCost(instructions, maxArgumentBytes int) time.Duration {
	return time.Duration(float64(worstCaseAtFullBudget) *
		(float64(instructions) / MaxTaxonomyInstructions) *
		(float64(maxArgumentBytes) / worstCaseBytes))
}

// TaxonomyWarnings returns the non-fatal findings for a configuration that
// ValidateTaxonomy accepts: today, a summed program length over
// WarnTaxonomyInstructions, with the estimated worst-case cost per call. It
// returns nil for an invalid or empty configuration. The package does not
// log; the caller logs these at startup.
func TaxonomyWarnings(cfg TaxonomyConfig) []FieldError {
	_, t := compileTaxonomy(cfg)
	return t.Warnings()
}

// Warnings returns the non-fatal findings of the configuration t was built
// from (see TaxonomyWarnings), or nil.
func (t *Taxonomy) Warnings() []FieldError {
	if t == nil || t.instructions <= WarnTaxonomyInstructions {
		return nil
	}
	return []FieldError{{
		Field: "tool_categories",
		Message: fmt.Sprintf("compiled globs and patterns total %d instructions, over the soft limit of %d; estimated worst-case cost per tool call about %v at max_argument_bytes %d",
			t.instructions, WarnTaxonomyInstructions, EstimatedWorstCaseCost(t.instructions, t.maxArgs).Round(time.Millisecond), t.maxArgs),
	}}
}

// Instructions returns the summed compiled program length of every glob and
// argument pattern, or 0 for a nil taxonomy.
func (t *Taxonomy) Instructions() int {
	if t == nil {
		return 0
	}
	return t.instructions
}

// ValidateTaxonomy checks the configuration against the bounds above and
// compiles every glob and pattern. It returns every problem, not just the
// first. Fields are relative to the correlation block, e.g.
// "tool_categories[0].tools[1]".
func ValidateTaxonomy(cfg TaxonomyConfig) []FieldError {
	errs, _ := compileTaxonomy(cfg)
	return errs
}

// NewTaxonomy validates and compiles the configuration once. No entries
// yields (nil, nil): a nil *Taxonomy categorizes nothing.
func NewTaxonomy(cfg TaxonomyConfig) (*Taxonomy, error) {
	errs, t := compileTaxonomy(cfg)
	if len(errs) > 0 {
		joined := make([]error, len(errs))
		for i := range errs {
			joined[i] = errs[i]
		}
		return nil, fmt.Errorf("invalid tool categories: %w", errors.Join(joined...))
	}
	return t, nil
}

// compileTaxonomy is the single validation and compilation path, so
// NewTaxonomy accepts exactly what ValidateTaxonomy accepts. It returns a
// nil taxonomy when there are errors or no entries.
func compileTaxonomy(cfg TaxonomyConfig) ([]FieldError, *Taxonomy) {
	var errs []FieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	maxArgs := cfg.MaxArgumentBytes
	if maxArgs == 0 {
		maxArgs = DefaultMaxArgumentBytes
	}
	if maxArgs < 1 || maxArgs > MaxArgumentBytesCeiling {
		add("max_argument_bytes", "must be between 1 and %d, got %d", MaxArgumentBytesCeiling, cfg.MaxArgumentBytes)
	}
	rules := cfg.ToolCategories
	if len(rules) > MaxTaxonomyEntries {
		add("tool_categories", "at most %d entries, got %d", MaxTaxonomyEntries, len(rules))
	}
	t := &Taxonomy{maxArgs: maxArgs}
	seen := make(map[string]bool)
	total := 0
	for i, r := range rules {
		p := fmt.Sprintf("tool_categories[%d]", i)
		c := compiledToolCategory{category: r.Category}
		if !ValidCategory(r.Category) {
			add(p+".category", "must be 1-64 lowercase letters, digits or underscores and not %q, got %q", CategoryMatch, r.Category)
		}
		switch {
		case len(r.Tools) == 0:
			add(p+".tools", "at least one tool name glob is required")
		case len(r.Tools) > MaxGlobsPerEntry:
			add(p+".tools", "at most %d globs, got %d", MaxGlobsPerEntry, len(r.Tools))
		default:
			for j, g := range r.Tools {
				f := fmt.Sprintf("%s.tools[%d]", p, j)
				switch {
				case g == "":
					add(f, "empty glob")
				case len(g) > MaxToolGlobBytes:
					add(f, "at most %d bytes, got %d", MaxToolGlobBytes, len(g))
				default:
					expr, err := globToRegexp(g)
					if err != nil {
						add(f, "invalid glob %q: %v", g, err)
						continue
					}
					re, n, err := compileBounded(expr)
					if err != nil {
						add(f, "%v", err)
						continue
					}
					total += n
					c.globs = append(c.globs, re)
				}
			}
		}
		if len(r.ArgumentPatterns) > MaxPatternsPerEntry {
			add(p+".argument_patterns", "at most %d patterns, got %d", MaxPatternsPerEntry, len(r.ArgumentPatterns))
		} else {
			for j, pat := range r.ArgumentPatterns {
				f := fmt.Sprintf("%s.argument_patterns[%d]", p, j)
				switch {
				case pat == "":
					add(f, "empty pattern; omit argument_patterns to match any arguments")
				case len(pat) > MaxArgumentPatternBytes:
					add(f, "at most %d bytes, got %d", MaxArgumentPatternBytes, len(pat))
				default:
					re, n, err := compileBounded("(?i)" + pat)
					if err != nil {
						add(f, "%v", err)
						continue
					}
					total += n
					c.patterns = append(c.patterns, re)
				}
			}
		}
		t.rules = append(t.rules, c)
		if !seen[r.Category] {
			seen[r.Category] = true
			t.categories = append(t.categories, r.Category)
		}
	}
	if total > MaxTaxonomyInstructions {
		add("tool_categories", "compiled globs and patterns total %d instructions, at most %d", total, MaxTaxonomyInstructions)
	}
	if len(errs) > 0 || len(rules) == 0 {
		return errs, nil
	}
	t.instructions = total
	return nil, t
}

// compileBounded compiles expr and returns its compiled program length,
// rejecting programs over MaxPatternInstructions. The program is the one
// regexp.Compile builds: Perl syntax, simplified.
func compileBounded(expr string) (*regexp.Regexp, int, error) {
	parsed, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid pattern: %w", err)
	}
	prog, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return nil, 0, fmt.Errorf("invalid pattern: %w", err)
	}
	n := len(prog.Inst)
	if n > MaxPatternInstructions {
		return nil, 0, fmt.Errorf("compiles to %d instructions, at most %d", n, MaxPatternInstructions)
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid pattern: %w", err)
	}
	return re, n, nil
}

// globToRegexp translates a tool-name glob to an anchored, case-insensitive
// RE2 expression in which '*' and '?' match any character, '/' and newline
// included. Every literal character is escaped.
func globToRegexp(g string) (string, error) {
	if !utf8.ValidString(g) {
		return "", errors.New("not valid UTF-8")
	}
	var b strings.Builder
	b.WriteString(`(?is)^(?:`)
	for i := 0; i < len(g); {
		switch g[i] {
		case '*':
			for i < len(g) && g[i] == '*' {
				i++
			}
			b.WriteString(`.*`)
		case '?':
			b.WriteString(`.`)
			i++
		case '\\':
			r, size := utf8.DecodeRuneInString(g[i+1:])
			if size == 0 {
				return "", errors.New("trailing backslash")
			}
			b.WriteString(regexp.QuoteMeta(string(r)))
			i += 1 + size
		case '[':
			n, err := writeGlobClass(&b, g[i+1:])
			if err != nil {
				return "", err
			}
			i += 1 + n
		default:
			r, size := utf8.DecodeRuneInString(g[i:])
			b.WriteString(regexp.QuoteMeta(string(r)))
			i += size
		}
	}
	b.WriteString(`)$`)
	return b.String(), nil
}

// writeGlobClass translates the class body s (after '[') and returns how
// many bytes of s it consumed, the closing ']' included. Members are
// emitted as \x{...} escapes so no glob character is special in the regexp.
func writeGlobClass(b *strings.Builder, s string) (int, error) {
	i := 0
	b.WriteByte('[')
	if i < len(s) && s[i] == '^' {
		b.WriteByte('^')
		i++
	}
	member := func() (rune, error) {
		if i >= len(s) {
			return 0, errors.New("unterminated character class")
		}
		switch s[i] {
		case '\\':
			r, size := utf8.DecodeRuneInString(s[i+1:])
			if size == 0 {
				return 0, errors.New("trailing backslash")
			}
			i += 1 + size
			return r, nil
		case '-', ']':
			return 0, fmt.Errorf("unescaped %q in character class", s[i])
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		return r, nil
	}
	empty := true
	for {
		if i >= len(s) {
			return 0, errors.New("unterminated character class")
		}
		if s[i] == ']' {
			if empty {
				return 0, errors.New("empty character class")
			}
			b.WriteByte(']')
			return i + 1, nil
		}
		lo, err := member()
		if err != nil {
			return 0, err
		}
		hi := lo
		if i < len(s) && s[i] == '-' {
			i++
			if hi, err = member(); err != nil {
				return 0, err
			}
			if hi < lo {
				return 0, errors.New("character range out of order")
			}
		}
		fmt.Fprintf(b, `\x{%x}-\x{%x}`, lo, hi)
		empty = false
	}
}

// Categorize returns the categories a tool call falls into, or nil. Every
// entry is evaluated, so a call can fall into several categories; they come
// back distinct, in the configuration order of the first entry that
// produced each. The result depends only on the configuration and the
// inputs. A name longer than MaxToolNameBytes falls into no category. Only
// the first max_argument_bytes of arguments are scanned. Neither the name
// nor the arguments are retained, logged or returned.
func (t *Taxonomy) Categorize(toolName, arguments string) []string {
	if t == nil || toolName == "" || len(toolName) > MaxToolNameBytes {
		return nil
	}
	if len(arguments) > t.maxArgs {
		// A cut inside a multi-byte rune leaves invalid UTF-8 at the end,
		// which RE2 reads as U+FFFD; only that last partial rune is affected.
		arguments = arguments[:t.maxArgs]
	}
	var out []string
	for _, r := range t.rules {
		if containsString(out, r.category) {
			continue // already produced: skip the scan
		}
		if !matchesAny(r.globs, toolName) {
			continue
		}
		if len(r.patterns) > 0 && !matchesAny(r.patterns, arguments) {
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

// MaxArgumentBytes returns how many bytes of arguments Categorize scans,
// or 0 for a nil taxonomy.
func (t *Taxonomy) MaxArgumentBytes() int {
	if t == nil {
		return 0
	}
	return t.maxArgs
}

func matchesAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
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
// With a provider call ID the identity is (session, call ID, tool name,
// category), so the same call reported twice, at any time or batch
// position, is one event, while a call ID reused for a different tool is
// not. Without one it is (session, tool name, category, time in UTC with
// nanoseconds, ordinal within the batch), so one anonymous call reported by
// two paths counts twice. The first canonical field ("call" or "anon")
// keeps the two forms apart. Every field is length-prefixed
// ("|<len>:<value>") after the version, the convention of MatchID and
// internal/decision identity v3. The material is identifiers only, never
// arguments.
//
// Callers must pass a callID only when the provider guarantees it unique
// within the session (OpenAI call_*, Anthropic toolu_*); otherwise they
// must pass "", because two distinct calls sharing a call ID, tool and
// category in one session are one event.
func ToolEventID(sessionID, callID, toolName, category string, at time.Time, ordinal int) string {
	var fields []string
	if callID != "" {
		fields = []string{"call", sessionID, callID, toolName, category}
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
