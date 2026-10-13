package unit

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"elida/internal/correlation"
)

func okLimits() correlation.Limits {
	return correlation.Limits{MaxHistoryEvents: correlation.DefaultMaxHistoryEvents, MaxSessions: correlation.DefaultMaxSessions}
}

func repeatedRule(name, cat string, count int, window time.Duration) correlation.Rule {
	return correlation.Rule{Name: name, Shape: correlation.ShapeRepeated, Category: cat, Count: count, Window: window}
}

func sequenceRule(name string, window time.Duration, steps ...string) correlation.Rule {
	return correlation.Rule{Name: name, Shape: correlation.ShapeSequence, Sequence: steps, Window: window}
}

func fieldsOf(errs []correlation.FieldError) []string {
	var out []string
	for _, e := range errs {
		out = append(out, e.Field)
	}
	return out
}

func TestCorrelationValidate_AcceptsBothShapes(t *testing.T) {
	rules := []correlation.Rule{
		repeatedRule("slow_burn_injection", "injection_elevated", 3, 15*time.Minute),
		sequenceRule("recon_then_send", 10*time.Minute, "credential_discovery", "external_transmission"),
	}
	if errs := correlation.Validate(rules, okLimits()); len(errs) != 0 {
		t.Fatalf("valid rules rejected: %v", errs)
	}
	if errs := correlation.Validate(nil, okLimits()); len(errs) != 0 {
		t.Fatalf("zero rules is valid (enabled but inert): %v", errs)
	}
}

func TestCorrelationValidate_Rejects(t *testing.T) {
	cases := []struct {
		name   string
		rule   correlation.Rule
		limits correlation.Limits
		field  string
	}{
		{"bad name", repeatedRule("Slow Burn", "a", 3, time.Minute), okLimits(), "rules[0].name"},
		{"unknown type", correlation.Rule{Name: "x", Shape: "chain", Window: time.Minute}, okLimits(), "rules[0].type"},
		{"repeated without category", repeatedRule("x", "", 3, time.Minute), okLimits(), "rules[0].category"},
		{"repeated on reserved category", repeatedRule("x", correlation.CategoryMatch, 3, time.Minute), okLimits(), "rules[0].category"},
		{"count below min", repeatedRule("x", "a", 1, time.Minute), okLimits(), "rules[0].count"},
		{"count above max", repeatedRule("x", "a", correlation.MaxRepeatCount+1, time.Minute), okLimits(), "rules[0].count"},
		{"bad count_by", func() correlation.Rule { r := repeatedRule("x", "a", 3, time.Minute); r.CountBy = "turn"; return r }(), okLimits(), "rules[0].count_by"},
		{"repeated with sequence", func() correlation.Rule {
			r := repeatedRule("x", "a", 3, time.Minute)
			r.Sequence = []string{"a", "b"}
			return r
		}(), okLimits(), "rules[0].sequence"},
		{"sequence too short", sequenceRule("x", time.Minute, "a"), okLimits(), "rules[0].sequence"},
		{"sequence too long", sequenceRule("x", time.Minute, "a", "b", "c", "d", "e", "f", "g", "h", "i"), okLimits(), "rules[0].sequence"},
		{"sequence step reserved", sequenceRule("x", time.Minute, "a", correlation.CategoryMatch), okLimits(), "rules[0].sequence[1]"},
		{"sequence with category", func() correlation.Rule { r := sequenceRule("x", time.Minute, "a", "b"); r.Category = "a"; return r }(), okLimits(), "rules[0].category"},
		{"sequence with count", func() correlation.Rule { r := sequenceRule("x", time.Minute, "a", "b"); r.Count = 2; return r }(), okLimits(), "rules[0].count"},
		{"zero window", repeatedRule("x", "a", 3, 0), okLimits(), "rules[0].window"},
		{"window above max", repeatedRule("x", "a", 3, correlation.MaxWindow+time.Second), okLimits(), "rules[0].window"},
		{"negative cooldown", func() correlation.Rule { r := repeatedRule("x", "a", 3, time.Minute); r.Cooldown = -1; return r }(), okLimits(), "rules[0].cooldown"},
		{"bad severity", func() correlation.Rule { r := repeatedRule("x", "a", 3, time.Minute); r.Severity = "high"; return r }(), okLimits(), "rules[0].severity"},
		{"bad mode", func() correlation.Rule { r := repeatedRule("x", "a", 3, time.Minute); r.Mode = "block"; return r }(), okLimits(), "rules[0].mode"},
		{"history zero", repeatedRule("x", "a", 3, time.Minute), correlation.Limits{MaxHistoryEvents: 0, MaxSessions: 10}, "max_history_events"},
		{"history above ceiling", repeatedRule("x", "a", 3, time.Minute), correlation.Limits{MaxHistoryEvents: correlation.MaxHistoryEventsCeiling + 1, MaxSessions: 10}, "max_history_events"},
		{"history smaller than rule", repeatedRule("x", "a", 8, time.Minute), correlation.Limits{MaxHistoryEvents: 4, MaxSessions: 10}, "max_history_events"},
		{"sessions zero", repeatedRule("x", "a", 3, time.Minute), correlation.Limits{MaxHistoryEvents: 64, MaxSessions: 0}, "max_sessions"},
		{"sessions above ceiling", repeatedRule("x", "a", 3, time.Minute), correlation.Limits{MaxHistoryEvents: 64, MaxSessions: correlation.MaxSessionsCeiling + 1}, "max_sessions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := correlation.Validate([]correlation.Rule{tc.rule}, tc.limits)
			for _, f := range fieldsOf(errs) {
				if f == tc.field {
					return
				}
			}
			t.Fatalf("want an error on %q, got %v", tc.field, errs)
		})
	}
}

func TestCorrelationValidate_DuplicateNamesAndTooManyRules(t *testing.T) {
	dup := []correlation.Rule{
		repeatedRule("same", "a", 3, time.Minute),
		repeatedRule("same", "b", 3, time.Minute),
	}
	if !strings.Contains(strings.Join(fieldsOf(correlation.Validate(dup, okLimits())), ","), "rules[1].name") {
		t.Fatal("a duplicate rule name must be rejected on the second rule")
	}
	var many []correlation.Rule
	for i := 0; i <= correlation.MaxRules; i++ {
		many = append(many, repeatedRule("r"+strings.Repeat("x", i%40)+string(rune('a'+i%26)), "a", 3, time.Minute))
	}
	if !strings.Contains(strings.Join(fieldsOf(correlation.Validate(many, okLimits())), ","), "rules") {
		t.Fatalf("more than %d rules must be rejected", correlation.MaxRules)
	}
}

func TestCorrelationMatchID_StableOrderedAndUnambiguous(t *testing.T) {
	a := correlation.MatchID("slow_burn", []string{"ev_1", "ev_2", "ev_3"})
	if !regexp.MustCompile(`^cm_[0-9a-f]{32}$`).MatchString(a) {
		t.Fatalf("MatchID = %q, want cm_ + 32 hex", a)
	}
	if b := correlation.MatchID("slow_burn", []string{"ev_1", "ev_2", "ev_3"}); b != a {
		t.Fatal("MatchID must be stable")
	}
	if b := correlation.MatchID("slow_burn", []string{"ev_2", "ev_1", "ev_3"}); b == a {
		t.Fatal("the contributing IDs are ordered: a different order is a different match")
	}
	if b := correlation.MatchID("other_rule", []string{"ev_1", "ev_2", "ev_3"}); b == a {
		t.Fatal("a different rule is a different match")
	}
	// Length prefixing: moving a boundary must change the ID.
	if correlation.MatchID("r", []string{"ab", "c"}) == correlation.MatchID("r", []string{"a", "bc"}) {
		t.Fatal("field boundaries must be unambiguous")
	}
	if correlation.MatchID("r|1:x", []string{"y"}) == correlation.MatchID("r", []string{"x", "y"}) {
		t.Fatal("a rule name must not be able to impersonate an event ID")
	}
}

func TestCorrelationValidCategory(t *testing.T) {
	for _, c := range []string{"injection_elevated", "prompt_injection", "credential_discovery", "a1"} {
		if !correlation.ValidCategory(c) {
			t.Errorf("%q should be a valid category", c)
		}
	}
	for _, c := range []string{"", "Injection", "has space", "dash-ed", correlation.CategoryMatch, strings.Repeat("a", 65)} {
		if correlation.ValidCategory(c) {
			t.Errorf("%q should be rejected", c)
		}
	}
	if correlation.RuleNamePrefix != "correlation." {
		t.Fatalf("RuleNamePrefix = %q; config reserves exactly this prefix", correlation.RuleNamePrefix)
	}
}

// TestCorrelationMatchID_Golden pins the v1 derivation. The values were
// computed independently from the documented canonical form
// ("v1" then "|<len>:<field>" for rule, event count and each event ID,
// hashed under "elida.correlation.v1\x00"). A change here re-keys every
// recorded match and needs a new version, not a new golden value.
func TestCorrelationMatchID_Golden(t *testing.T) {
	cases := []struct {
		rule string
		ids  []string
		want string
	}{
		{"slow_burn", []string{"ev_1", "ev_2", "ev_3"}, "cm_62fa8b11c8593b122850ca1a15e0971e"},
		{"recon_then_send", []string{"ev_a", "te_b"}, "cm_e106c6631c01c1cf4b8cdc7daad0cf5d"},
		{"r", nil, "cm_b06c441164492647b9b90d6d665a5d13"},
	}
	for _, tc := range cases {
		if got := correlation.MatchID(tc.rule, tc.ids); got != tc.want {
			t.Errorf("MatchID(%q, %v) = %s, want %s", tc.rule, tc.ids, got, tc.want)
		}
	}
}
