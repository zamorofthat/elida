package unit

import (
	"reflect"
	"testing"
	"time"

	"elida/internal/decision"
)

func TestDecisionContract_SignalAndScopeValues(t *testing.T) {
	// These strings go into stored events, telemetry and the dashboard.
	// Renaming one is a breaking change, so they are pinned here.
	if decision.SignalInjection != "injection" {
		t.Errorf("SignalInjection = %q", decision.SignalInjection)
	}
	if decision.SignalHumanDirected != "human_directed" {
		t.Errorf("SignalHumanDirected = %q", decision.SignalHumanDirected)
	}
	if decision.SignalCompliance != "compliance" {
		t.Errorf("SignalCompliance = %q", decision.SignalCompliance)
	}
	if decision.DirectionRequest != "request" || decision.DirectionResponse != "response" {
		t.Error("direction constants drifted")
	}
	if decision.ScopeCurrentRequest != "current_request" {
		t.Errorf("ScopeCurrentRequest = %q", decision.ScopeCurrentRequest)
	}
	if decision.ScopeFutureActivity != "future_activity" {
		t.Errorf("ScopeFutureActivity = %q", decision.ScopeFutureActivity)
	}
	if decision.ScopeRemainingStream != "remaining_stream" {
		t.Errorf("ScopeRemainingStream = %q", decision.ScopeRemainingStream)
	}
}

func TestDecisionContract_UnansweredIsNeverSafe(t *testing.T) {
	// An unanswered signal must not read as a low probability. The only way
	// to learn a signal's probability is MaxProbability, which reports
	// answered=false when nothing answered, whatever the Probability field
	// happens to hold.
	a := decision.Assessment{
		Decisions: []decision.Decision{
			{Signal: decision.SignalInjection, Probability: 0, Answered: false},
		},
	}
	if _, answered := a.MaxProbability(decision.SignalInjection); answered {
		t.Fatal("an unanswered decision must not count as an answer")
	}

	a.Decisions = append(a.Decisions, decision.Decision{
		Signal: decision.SignalInjection, Probability: 0.42, Answered: true,
	})
	p, answered := a.MaxProbability(decision.SignalInjection)
	if !answered || p != 0.42 {
		t.Fatalf("MaxProbability = (%v, %v), want (0.42, true)", p, answered)
	}

	// A signal nobody was asked about is unanswered, not safe.
	if _, answered := a.MaxProbability(decision.SignalCompliance); answered {
		t.Fatal("a signal with no decisions must be unanswered")
	}
}

func TestDecisionContract_MaxProbabilityTakesTheMaximum(t *testing.T) {
	// "The content-level signal is the maximum probability among completed
	// eligible windows."
	a := decision.Assessment{Decisions: []decision.Decision{
		{Signal: decision.SignalInjection, Probability: 0.11, Answered: true},
		{Signal: decision.SignalInjection, Probability: 0.87, Answered: true},
		{Signal: decision.SignalInjection, Probability: 0.44, Answered: true},
		{Signal: decision.SignalHumanDirected, Probability: 0.99, Answered: true},
	}}
	p, answered := a.MaxProbability(decision.SignalInjection)
	if !answered || p != 0.87 {
		t.Fatalf("MaxProbability(injection) = (%v, %v), want (0.87, true)", p, answered)
	}
	// Signals must not bleed into each other.
	p, answered = a.MaxProbability(decision.SignalHumanDirected)
	if !answered || p != 0.99 {
		t.Fatalf("MaxProbability(human_directed) = (%v, %v), want (0.99, true)", p, answered)
	}
}

// TestDecisionContract_ByWindowKeepsTheVetoPerWindow pins the accessor the
// human_directed veto must use. The veto applies only to the injection score
// from the same window, so the two signals have to stay paired per window: a
// content-level maximum would pair an injecting window's 0.9 with a benign
// window's 0.9 human_directed score and veto a real detection.
func TestDecisionContract_ByWindowKeepsTheVetoPerWindow(t *testing.T) {
	injecting := decision.Window{StartByte: 0, EndByte: 100}
	benign := decision.Window{StartByte: 100, EndByte: 200}

	a := decision.Assessment{Decisions: []decision.Decision{
		{Signal: decision.SignalInjection, Probability: 0.9, Answered: true, Window: injecting},
		{Signal: decision.SignalHumanDirected, Probability: 0.1, Answered: true, Window: injecting},
		{Signal: decision.SignalInjection, Probability: 0.2, Answered: true, Window: benign},
		{Signal: decision.SignalHumanDirected, Probability: 0.9, Answered: true, Window: benign},
	}}

	inj := a.ByWindow(decision.SignalInjection)
	hd := a.ByWindow(decision.SignalHumanDirected)
	if len(inj) != 2 || len(hd) != 2 {
		t.Fatalf("expected both signals keyed by both windows, got %d injection and %d human_directed", len(inj), len(hd))
	}
	for _, tc := range []struct {
		name      string
		w         decision.Window
		injection float64
		human     float64
	}{
		{"injecting window", injecting, 0.9, 0.1},
		{"benign window", benign, 0.2, 0.9},
	} {
		if got := inj[tc.w].Probability; got != tc.injection {
			t.Errorf("%s: injection = %v, want %v", tc.name, got, tc.injection)
		}
		if got := hd[tc.w].Probability; got != tc.human {
			t.Errorf("%s: human_directed = %v, want %v", tc.name, got, tc.human)
		}
	}

	// The content-level view cannot express the pairing, which is why the
	// veto may not be evaluated from it: both maxima are 0.9.
	if p, _ := a.MaxProbability(decision.SignalInjection); p != 0.9 {
		t.Fatalf("MaxProbability(injection) = %v, want 0.9", p)
	}
	if p, _ := a.MaxProbability(decision.SignalHumanDirected); p != 0.9 {
		t.Fatalf("MaxProbability(human_directed) = %v, want 0.9", p)
	}

	// An unanswered decision is absent, never a zero probability.
	a.Decisions = append(a.Decisions, decision.Decision{
		Signal:   decision.SignalCompliance,
		Window:   injecting,
		Answered: false,
	})
	if got := a.ByWindow(decision.SignalCompliance); len(got) != 0 {
		t.Fatalf("an unanswered decision must not appear in ByWindow, got %+v", got)
	}

	// Transform and TransformDepth are part of the key: the same byte range
	// scored through a derived representation is a different window.
	derived := decision.Window{StartByte: 0, EndByte: 100, Transform: "base64_decode", TransformDepth: 1}
	a.Decisions = append(a.Decisions, decision.Decision{
		Signal: decision.SignalInjection, Probability: 0.77, Answered: true, Window: derived,
	})
	inj = a.ByWindow(decision.SignalInjection)
	if len(inj) != 3 {
		t.Fatalf("a derived representation is its own window, got %d keys", len(inj))
	}
	if got := inj[derived].Probability; got != 0.77 {
		t.Fatalf("derived window: injection = %v, want 0.77", got)
	}
	if got := inj[injecting].Probability; got != 0.9 {
		t.Fatalf("the original window must be untouched, got %v", got)
	}
}

func TestDecisionContract_CoverageIncompleteUnlessEverythingScored(t *testing.T) {
	cases := []struct {
		name string
		c    decision.Coverage
		want bool
	}{
		{"all windows scored inline", decision.Coverage{EligibleWindows: 3, ScoredInline: 3, EligibleBytes: 90, ScoredBytes: 90}, true},
		{"some queued async", decision.Coverage{EligibleWindows: 3, ScoredInline: 1, QueuedAsync: 2, EligibleBytes: 90, ScoredBytes: 30}, false},
		{"bytes short", decision.Coverage{EligibleWindows: 1, ScoredInline: 1, EligibleBytes: 90, ScoredBytes: 30}, false},
		{"nothing eligible", decision.Coverage{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.IsComplete(); got != tc.want {
				t.Fatalf("IsComplete() = %v, want %v for %+v", got, tc.want, tc.c)
			}
		})
	}
}

func TestDecisionContract_InputCarriesNoIdentifiers(t *testing.T) {
	// Session and request IDs stay in the orchestration layer so an HTTP
	// provider cannot receive internal identifiers. A keyed struct literal
	// below would still compile if someone added a SessionID or RequestID
	// field, so this asserts the exact field set via reflection instead:
	// that is what actually guards the privacy boundary.
	wantFields := []string{"Content", "Direction", "SourceRole", "MessageIndex"}
	typ := reflect.TypeOf(decision.Input{})
	if typ.NumField() != len(wantFields) {
		t.Fatalf("decision.Input has %d fields, want %d %v", typ.NumField(), len(wantFields), wantFields)
	}
	for i, name := range wantFields {
		if got := typ.Field(i).Name; got != name {
			t.Fatalf("decision.Input field %d = %q, want %q", i, got, name)
		}
	}

	in := decision.Input{
		Content:      "hello",
		Direction:    decision.DirectionRequest,
		SourceRole:   "user",
		MessageIndex: 2,
	}
	if in.Content != "hello" || in.MessageIndex != 2 {
		t.Fatal("Input field round-trip failed")
	}
	// Compile-time proof that Decision carries latency (health reports it).
	d := decision.Decision{Latency: 7 * time.Millisecond}
	if d.Latency != 7*time.Millisecond {
		t.Fatal("Decision.Latency round-trip failed")
	}
}
