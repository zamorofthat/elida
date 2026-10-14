package correlation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Shape is a correlation rule's kind.
type Shape string

const (
	// ShapeRepeated matches at least Count events of one category within
	// Window.
	ShapeRepeated Shape = "repeated"
	// ShapeSequence matches the categories of Sequence, in order, within
	// Window.
	ShapeSequence Shape = "sequence"
)

// CountBy says what a repeated rule counts.
type CountBy string

const (
	// CountByEvent counts every event.
	CountByEvent CountBy = "event"
	// CountByMessage counts distinct Event.Group values, so several events
	// produced by one message (its windows, its decoded representations)
	// count once. A message that contributed to a match of the rule is
	// consumed for that rule: none of its events count toward another match
	// of the rule unless the event is more than Window after that match's
	// last event (exactly Window later is still consumed). This holds after
	// the message's earlier events have left the history.
	CountByMessage CountBy = "message"
)

// Mode says whether a rule's matches contribute risk.
type Mode string

const (
	// ModeAudit matches are evidence-only. This is the default.
	ModeAudit Mode = "audit"
	// ModeEnforce matches are ordinary violations that contribute risk
	// through the existing ladder. Promoting a rule to enforce is a
	// reserved decision for senior/security review.
	ModeEnforce Mode = "enforce"
)

// CategoryMatch is the event category a recorded match carries. It is never
// an input: Validate rejects rules that reference it, and the engine ignores
// events that carry it, so evaluation cannot feed on its own output.
const CategoryMatch = "correlation_match"

// RuleNamePrefix prefixes the policy RuleName a match is recorded under
// ("correlation." + rule). Config reserves it: no policy rule may start with
// it, so a match never shares a display entry with a config rule.
const RuleNamePrefix = "correlation."

// Ceilings that Validate enforces. Raising any of them weakens a bound and
// is a reserved decision.
const (
	MaxRules                = 64
	MinSequenceSteps        = 2
	MaxSequenceSteps        = 8
	MinRepeatCount          = 2
	MaxRepeatCount          = 32
	MaxWindow               = 24 * time.Hour
	MaxHistoryEventsCeiling = 1024
	MaxSessionsCeiling      = 100000
	// MaxMatchIDsPerSession caps the per-session set of emitted match IDs
	// kept for deduplication (as 16-byte digests). Oldest evicted first.
	MaxMatchIDsPerSession = 256
	// DefaultMaxHistoryEvents and DefaultMaxSessions are the configuration
	// defaults.
	//
	// Worst-case memory, measured as live heap per session after a GC:
	// every rule matching repeatedly (count 2, 1 ns cooldown), a full
	// history of 38-byte event IDs, a full dedup set and, under count_by
	// message, a full consumed-group set:
	//
	//	history  rules  count_by  per session  x 4096 (default)  x 100000 (ceiling)
	//	64       64     event     ~29 KB       ~117 MB           ~2.9 GB
	//	64       64     message   ~39 KB       ~159 MB           ~3.9 GB
	//	64       1      message   ~37 KB       ~153 MB           ~3.7 GB
	//	1024     64     event     ~196 KB      ~803 MB           ~19.6 GB
	//	1024     64     message   ~295 KB      ~1.2 GB           ~29.5 GB
	//
	// The history costs about 180 B per event (the entry plus its ID,
	// category and group strings; longer IDs cost more), the dedup set about
	// 12 KB when full, the consumed-group set about 80 B per group, and the
	// last-match times 24 B per rule. A session that has never matched
	// carries only its history. Operators raising max_history_events or
	// max_sessions should budget from this table.
	DefaultMaxHistoryEvents = 64
	DefaultMaxSessions      = 4096
)

// Rule is one correlation rule. Validate it before use; New normalizes the
// zero values (CountBy event, Cooldown = Window, Severity warning, Mode
// audit).
type Rule struct {
	Name  string `yaml:"name" json:"name"`
	Shape Shape  `yaml:"shape" json:"shape"`
	// Category, Count and CountBy apply to repeated rules only.
	Category string  `yaml:"category,omitempty" json:"category,omitempty"`
	Count    int     `yaml:"count,omitempty" json:"count,omitempty"`
	CountBy  CountBy `yaml:"count_by,omitempty" json:"count_by,omitempty"`
	// Sequence applies to sequence rules only: categories in order.
	Sequence []string `yaml:"sequence,omitempty" json:"sequence,omitempty"`
	// Window bounds the time from the first to the last contributing event.
	// It is inclusive: events exactly Window apart may match. Under
	// count_by message it is also how long a consumed message stays
	// consumed (see CountByMessage).
	Window time.Duration `yaml:"window" json:"window"`
	// Cooldown suppresses a further match of this rule in a session until
	// this long after the previous match's last event. It is inclusive on
	// the allowed side: a candidate whose last event is exactly Cooldown
	// after the previous match's last event may match. Zero means Window.
	// Each candidate window is checked separately, so a late event can
	// still complete a window that ends after the cooldown.
	Cooldown time.Duration `yaml:"cooldown,omitempty" json:"cooldown,omitempty"`
	// Severity is "info", "warning" or "critical"; empty means warning.
	Severity string `yaml:"severity,omitempty" json:"severity,omitempty"`
	Mode     Mode   `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// Limits bounds the engine's memory.
type Limits struct {
	// MaxHistoryEvents caps each session's history. The budget is shared
	// by every category any rule wants: a high-volume category can evict a
	// low-volume category's evidence before that evidence's rule window
	// has elapsed, so a flood of one wanted category is an evasion lever
	// against rules on another. With the default rule set (one category)
	// this does not arise. Follow-up, before tool-call categories ship
	// (Plan B Task 7): reserve a share of the budget for
	// injection_elevated, or give each rule its own budget.
	MaxHistoryEvents int
	// MaxSessions caps the sessions tracked across all shards (see
	// MaxShards).
	MaxSessions int
}

// Event is one correlation input. It carries no content.
type Event struct {
	// ID is the stable event ID (a policy ViolationEvent's EventID, or a
	// ToolEventID). Events with an empty ID are ignored; an ID already in
	// the session's history is a duplicate and is ignored.
	ID       string
	Category string
	// Group identifies the message an event came from (source role and
	// message index). Empty means the event is its own group.
	Group     string
	Timestamp time.Time
}

// Match is one rule match. It carries no content.
type Match struct {
	// ID is MatchID(Rule, EventIDs).
	ID       string
	Rule     string
	Shape    Shape
	Severity string
	Mode     Mode
	// EventIDs and Categories are the contributing events in time order.
	EventIDs   []string
	Categories []string
	FirstEvent time.Time
	LastEvent  time.Time
}

// FieldError is one validation failure. Field is relative to the
// correlation configuration block, e.g. "rules[0].count".
type FieldError struct {
	Field   string
	Message string
}

// Error renders the failure as "<field>: <message>".
func (f FieldError) Error() string { return f.Field + ": " + f.Message }

var categoryPattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// ValidCategory reports whether c can be used as a correlation category:
// lowercase letters, digits and underscores, 1-64 bytes, and not the
// reserved CategoryMatch.
func ValidCategory(c string) bool {
	return c != CategoryMatch && categoryPattern.MatchString(c)
}

// Validate checks rules and limits against the ceilings above. It returns
// every problem, not just the first.
func Validate(rules []Rule, limits Limits) []FieldError {
	var errs []FieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	historyOK := limits.MaxHistoryEvents >= 1 && limits.MaxHistoryEvents <= MaxHistoryEventsCeiling
	if !historyOK {
		add("max_history_events", "must be between 1 and %d, got %d", MaxHistoryEventsCeiling, limits.MaxHistoryEvents)
	}
	if limits.MaxSessions < 1 || limits.MaxSessions > MaxSessionsCeiling {
		add("max_sessions", "must be between 1 and %d, got %d", MaxSessionsCeiling, limits.MaxSessions)
	}
	if len(rules) > MaxRules {
		add("rules", "at most %d rules, got %d", MaxRules, len(rules))
	}

	names := make(map[string]bool, len(rules))
	for i, r := range rules {
		p := fmt.Sprintf("rules[%d]", i)
		switch {
		case !categoryPattern.MatchString(r.Name):
			add(p+".name", "must be 1-64 lowercase letters, digits or underscores, got %q", r.Name)
		case names[r.Name]:
			add(p+".name", "duplicate rule name %q", r.Name)
		}
		names[r.Name] = true

		needed := 0
		switch r.Shape {
		case ShapeRepeated:
			if !ValidCategory(r.Category) {
				add(p+".category", "must be a valid category other than %q, got %q", CategoryMatch, r.Category)
			}
			if len(r.Sequence) > 0 {
				add(p+".sequence", "not used by a repeated rule")
			}
			if r.Count < MinRepeatCount || r.Count > MaxRepeatCount {
				add(p+".count", "must be between %d and %d, got %d", MinRepeatCount, MaxRepeatCount, r.Count)
			}
			switch r.CountBy {
			case "", CountByEvent, CountByMessage:
			default:
				add(p+".count_by", "must be %q or %q, got %q", CountByEvent, CountByMessage, r.CountBy)
			}
			needed = r.Count
		case ShapeSequence:
			if r.Category != "" {
				add(p+".category", "not used by a sequence rule; list the steps under sequence")
			}
			if r.Count != 0 {
				add(p+".count", "not used by a sequence rule")
			}
			if r.CountBy != "" {
				add(p+".count_by", "not used by a sequence rule")
			}
			if len(r.Sequence) < MinSequenceSteps || len(r.Sequence) > MaxSequenceSteps {
				add(p+".sequence", "must have %d to %d steps, got %d", MinSequenceSteps, MaxSequenceSteps, len(r.Sequence))
			}
			for j, c := range r.Sequence {
				if !ValidCategory(c) {
					add(fmt.Sprintf("%s.sequence[%d]", p, j), "must be a valid category other than %q, got %q", CategoryMatch, c)
				}
			}
			needed = len(r.Sequence)
		default:
			add(p+".type", "must be %q or %q, got %q", ShapeRepeated, ShapeSequence, r.Shape)
		}
		if historyOK && needed > limits.MaxHistoryEvents {
			add("max_history_events", "must be at least %d to hold rule %q", needed, r.Name)
		}

		if r.Window <= 0 || r.Window > MaxWindow {
			add(p+".window", "must be greater than 0 and at most %s, got %s", MaxWindow, r.Window)
		}
		if r.Cooldown < 0 || r.Cooldown > MaxWindow {
			add(p+".cooldown", "must be between 0 (means window) and %s, got %s", MaxWindow, r.Cooldown)
		}
		switch r.Severity {
		case "", "info", "warning", "critical":
		default:
			add(p+".severity", "must be info, warning or critical, got %q", r.Severity)
		}
		switch r.Mode {
		case "", ModeAudit, ModeEnforce:
		default:
			add(p+".mode", "must be %q or %q, got %q", ModeAudit, ModeEnforce, r.Mode)
		}
	}
	return errs
}

// matchIDVersion opens every canonical match string and names the hash
// domain, so an ID derived under a later form can never equal one derived
// under this one.
const matchIDVersion = "v1"

// canonicalMatch renders (rule, ordered event IDs) unambiguously: the
// version, then every field length-prefixed ("<len>:<value>") and preceded
// by "|", with the event count as its own field. This is the
// internal/decision identity v3 convention.
func canonicalMatch(rule string, eventIDs []string) string {
	var b strings.Builder
	b.WriteString(matchIDVersion)
	write := func(f string) {
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	write(rule)
	write(strconv.Itoa(len(eventIDs)))
	for _, id := range eventIDs {
		write(id)
	}
	return b.String()
}

// MatchID is the stable ID of a match: "cm_" followed by the first 16 bytes
// of the SHA-256 of the canonical (rule, ordered contributing event IDs),
// hex-encoded. Event IDs are already session-scoped, so the session is not
// repeated here. The same rule over the same events always yields the same
// ID, which is what deduplicates repeated evaluation.
func MatchID(rule string, eventIDs []string) string {
	return matchIDFromDigest(matchDigest(rule, eventIDs))
}

// matchDigest is the 16-byte digest a MatchID encodes. The engine keeps
// digests, not ID strings, in its per-session dedup set.
func matchDigest(rule string, eventIDs []string) [16]byte {
	sum := sha256.Sum256([]byte("elida.correlation." + matchIDVersion + "\x00" + canonicalMatch(rule, eventIDs)))
	var d [16]byte
	copy(d[:], sum[:16])
	return d
}

func matchIDFromDigest(d [16]byte) string {
	return "cm_" + hex.EncodeToString(d[:])
}
