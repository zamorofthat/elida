package proxy

import (
	"reflect"
	"regexp"
	"testing"

	"elida/internal/policy"
	"elida/internal/session"
)

// TestExtractRequestMessages_PolicyViewUnchanged pins the policy engine's
// input (EvaluateMessages) for a request mixing every content shape. The
// expected slice is what extractScannableMessages returned before
// tool_result extraction existed; collecting tool results must not change
// it by a byte.
func TestExtractRequestMessages_PolicyViewUnchanged(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"sys <system-reminder>hidden</system-reminder>prompt"}],"messages":[` +
		`{"role":"user","content":"hello"},` +
		`{"role":"assistant","content":[{"type":"text","text":"calling"},{"type":"tool_use","id":"a","name":"read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"tool out"}]},{"type":"text","text":"next"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"only tool"}]},` +
		`{"role":"tool","content":"openai tool"}]}`)
	tags := []*regexp.Regexp{regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)}

	want := []policy.MessageToScan{
		{Role: "system", Index: -1, Content: "sys prompt "},
		{Role: "user", Index: 0, Content: "hello"},
		{Role: "assistant", Index: 1, Content: "calling "},
		{Role: "user", Index: 2, Content: "next "},
		{Role: "tool", Index: 4, Content: "openai tool"},
	}

	sessA := session.NewSession("a", "b", "c")
	before, noTools := extractRequestMessages(body, sessA, tags, false)
	sessB := session.NewSession("b", "b", "c")
	after, tools := extractRequestMessages(body, sessB, tags, true)

	if !reflect.DeepEqual(before, want) {
		t.Fatalf("policy view without tool results:\n got %#v\nwant %#v", before, want)
	}
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("collecting tool results changed the policy view:\n got %#v\nwant %#v", after, want)
	}
	if noTools != nil {
		t.Fatalf("tool results must not be collected when not asked: %#v", noTools)
	}
	wantTools := []policy.MessageToScan{
		{Role: "tool", Index: 2, Content: "tool out "},
		{Role: "tool", Index: 3, Content: "only tool "},
	}
	if !reflect.DeepEqual(tools, wantTools) {
		t.Fatalf("tool results:\n got %#v\nwant %#v", tools, wantTools)
	}

	merged := semanticMessages(after, tools)
	var order []int
	for _, m := range merged {
		order = append(order, m.Index)
	}
	if !reflect.DeepEqual(order, []int{-1, 0, 1, 2, 2, 3, 4}) || merged[4].Role != "tool" {
		t.Fatalf("merged order = %v (%#v)", order, merged)
	}
	if !reflect.DeepEqual(after, want) {
		t.Fatal("semanticMessages must not modify its inputs")
	}
}
