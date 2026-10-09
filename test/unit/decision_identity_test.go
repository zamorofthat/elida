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
		"session":    func(i *decision.Identity) { i.SessionID = "sess-xyz" },
		"request":    func(i *decision.Identity) { i.RequestID = "req-2" },
		"message":    func(i *decision.Identity) { i.MessageIndex = 4 },
		"role":       func(i *decision.Identity) { i.SourceRole = "tool" },
		"start":      func(i *decision.Identity) { i.StartByte = 11 },
		"end":        func(i *decision.Identity) { i.EndByte = 201 },
		"localStart": func(i *decision.Identity) { i.LocalStartByte = 1 },
		"localEnd":   func(i *decision.Identity) { i.LocalEndByte = 1 },
		"transform":  func(i *decision.Identity) { i.TransformChain = "nfkc>hex" },
		"signal":     func(i *decision.Identity) { i.Signal = decision.SignalHumanDirected },
		"model":      func(i *decision.Identity) { i.ModelVersion = "minilm-multihead-v6" },
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
	w := decision.Window{StartByte: 5, EndByte: 50, LocalStartByte: 7, LocalEndByte: 9, Transform: "rot13", TransformDepth: 1}
	got := id.WithWindow(w)
	if got.StartByte != 5 || got.EndByte != 50 || got.LocalStartByte != 7 || got.LocalEndByte != 9 || got.TransformChain != "rot13" {
		t.Fatalf("WithWindow did not copy the window: %+v", got)
	}
	// It must not mutate the receiver.
	if id.StartByte != 0 || id.TransformChain != "" {
		t.Fatalf("WithWindow mutated its receiver: %+v", id)
	}
}

func TestDecisionID_V2NeverEqualsV1(t *testing.T) {
	// These are the v1 IDs for these identities, captured from the v1 form
	// (no local offsets, "elida.*.v1" domains). A v2 ID over the same
	// fields must differ, so a stored v1 ID can never be mistaken for v2.
	const (
		v1Decision = "dec_ee89e4324a9297dcb650eb26024a5e35"
		v1Job      = "job_c11040d70bd204365cf1fd3458296308"
	)
	if got := decision.DecisionID(baseIdentity()); got == v1Decision {
		t.Fatalf("v2 DecisionID equals the v1 value %q", got)
	}
	j := decision.JobIdentity{SessionID: "s", RequestID: "r", MessageIndex: 2, StartByte: 0, EndByte: 100}
	if got := decision.JobID(j); got == v1Job {
		t.Fatalf("v2 JobID equals the v1 value %q", got)
	}
}

func TestDecisionID_V3NeverEqualsV2(t *testing.T) {
	// These are the v2 IDs for these identities, captured from the v2 form
	// (no source role, "elida.*.v2" domains). A v3 ID over the same fields
	// must differ, so a stored v2 ID can never be mistaken for v3.
	const (
		v2Decision = "dec_6f8cea9a0f0a3805bc57b618ffa84c4f"
		v2Job      = "job_445d7945b588530640266336e9703a0c"
	)
	if got := decision.DecisionID(baseIdentity()); got == v2Decision {
		t.Fatalf("v3 DecisionID equals the v2 value %q", got)
	}
	j := decision.JobIdentity{SessionID: "s", RequestID: "r", MessageIndex: 2, StartByte: 0, EndByte: 100}
	if got := decision.JobID(j); got == v2Job {
		t.Fatalf("v3 JobID equals the v2 value %q", got)
	}
}

func TestDecisionID_SourceRoleSeparatesSameIndexInputs(t *testing.T) {
	// An Anthropic user message's text and its tool_result content share a
	// message index; equal window offsets must still give distinct IDs.
	user := baseIdentity()
	user.SourceRole = "user"
	tool := user
	tool.SourceRole = "tool"
	if decision.DecisionID(user) == decision.DecisionID(tool) {
		t.Fatal("user and tool inputs at one index must have distinct decision IDs")
	}
	ju := decision.JobIdentity{SessionID: "s", RequestID: "r", MessageIndex: 2, SourceRole: "user", EndByte: 20, LocalEndByte: 20}
	jt := ju
	jt.SourceRole = "tool"
	if decision.JobID(ju) == decision.JobID(jt) {
		t.Fatal("user and tool inputs at one index must have distinct job IDs")
	}
}

func TestDecisionID_LocalOffsetsSeparateDerivedWindows(t *testing.T) {
	// The windows of one derived representation share the ancestor's
	// original range; only the local offsets tell them apart.
	id := baseIdentity()
	a := id.WithWindow(decision.Window{StartByte: 0, EndByte: 300, LocalStartByte: 0, LocalEndByte: 100, Transform: "base64_decode", TransformDepth: 1})
	b := id.WithWindow(decision.Window{StartByte: 0, EndByte: 300, LocalStartByte: 100, LocalEndByte: 200, Transform: "base64_decode", TransformDepth: 1})
	if decision.DecisionID(a) == decision.DecisionID(b) {
		t.Fatal("two windows of one derived representation must have distinct decision IDs")
	}
	ja := decision.JobIdentity{SessionID: "s", RequestID: "r", StartByte: 0, EndByte: 300, LocalStartByte: 0, LocalEndByte: 100, TransformChain: "base64_decode"}
	jb := ja
	jb.LocalStartByte, jb.LocalEndByte = 100, 200
	if decision.JobID(ja) == decision.JobID(jb) {
		t.Fatal("two windows of one derived representation must have distinct job IDs")
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
