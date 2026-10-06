package unit

import (
	"regexp"
	"testing"

	"elida/internal/decision"
)

func baseIdentity() decision.Identity {
	return decision.Identity{
		SessionID:      "sess-abc",
		RequestID:      "req-1",
		MessageIndex:   3,
		StartByte:      10,
		EndByte:        200,
		TransformChain: "nfkc>base64",
		Signal:         decision.SignalInjection,
		ModelVersion:   "minilm-multihead-v5",
	}
}

func TestDecisionID_Stable(t *testing.T) {
	want := decision.DecisionID(baseIdentity())
	for i := 0; i < 50; i++ {
		if got := decision.DecisionID(baseIdentity()); got != want {
			t.Fatalf("DecisionID is not stable: %q != %q on iteration %d", got, want, i)
		}
	}
	if !regexp.MustCompile(`^dec_[0-9a-f]{32}$`).MatchString(want) {
		t.Fatalf("DecisionID format = %q, want dec_ + 32 hex chars", want)
	}
}

func TestDecisionID_UniquePerField(t *testing.T) {
	base := decision.DecisionID(baseIdentity())
	mutations := map[string]func(*decision.Identity){
		"session":   func(i *decision.Identity) { i.SessionID = "sess-xyz" },
		"request":   func(i *decision.Identity) { i.RequestID = "req-2" },
		"message":   func(i *decision.Identity) { i.MessageIndex = 4 },
		"start":     func(i *decision.Identity) { i.StartByte = 11 },
		"end":       func(i *decision.Identity) { i.EndByte = 201 },
		"transform": func(i *decision.Identity) { i.TransformChain = "nfkc>hex" },
		"signal":    func(i *decision.Identity) { i.Signal = decision.SignalHumanDirected },
		"model":     func(i *decision.Identity) { i.ModelVersion = "minilm-multihead-v6" },
	}
	seen := map[string]string{base: "base"}
	for name, mutate := range mutations {
		id := baseIdentity()
		mutate(&id)
		got := decision.DecisionID(id)
		if got == base {
			t.Errorf("changing %s did not change the decision ID", name)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("changing %s collides with %s: %q", name, prev, got)
		}
		seen[got] = name
	}
}

func TestDecisionID_FieldsAreNotConcatenatedAmbiguously(t *testing.T) {
	// A naive "join with -" derivation makes these two identities collide.
	// Any separator-free or ambiguous encoding must fail here.
	a := baseIdentity()
	a.SessionID = "sess"
	a.RequestID = "abc-req-1"

	b := baseIdentity()
	b.SessionID = "sess-abc"
	b.RequestID = "req-1"

	if decision.DecisionID(a) == decision.DecisionID(b) {
		t.Fatal("identity encoding is ambiguous across field boundaries")
	}
}

func TestDecisionID_WithWindow(t *testing.T) {
	id := decision.Identity{
		SessionID: "s", RequestID: "r", MessageIndex: 1,
		Signal: decision.SignalInjection, ModelVersion: "m",
	}
	w := decision.Window{StartByte: 5, EndByte: 50, Transform: "rot13", TransformDepth: 1}
	got := id.WithWindow(w)
	if got.StartByte != 5 || got.EndByte != 50 || got.TransformChain != "rot13" {
		t.Fatalf("WithWindow did not copy the window: %+v", got)
	}
	// It must not mutate the receiver.
	if id.StartByte != 0 || id.TransformChain != "" {
		t.Fatalf("WithWindow mutated its receiver: %+v", id)
	}
}

func TestJobID_IgnoresSignalAndModel(t *testing.T) {
	// One job scores every requested signal for one window with one model,
	// so the job key must not vary with signal or model version — that is
	// what lets inline and async completion for the same work deduplicate.
	j := decision.JobIdentity{
		SessionID: "s", RequestID: "r", MessageIndex: 2,
		StartByte: 0, EndByte: 100, TransformChain: "",
	}
	a := decision.JobID(j)
	if !regexp.MustCompile(`^job_[0-9a-f]{32}$`).MatchString(a) {
		t.Fatalf("JobID format = %q, want job_ + 32 hex chars", a)
	}
	j2 := j
	j2.StartByte = 1
	if decision.JobID(j2) == a {
		t.Fatal("changing the window must change the job ID")
	}
	// A decision ID and a job ID for the same window must not be confusable.
	if a == decision.DecisionID(decision.Identity{
		SessionID: "s", RequestID: "r", MessageIndex: 2, StartByte: 0, EndByte: 100,
	}) {
		t.Fatal("job and decision IDs must live in different namespaces")
	}
}
