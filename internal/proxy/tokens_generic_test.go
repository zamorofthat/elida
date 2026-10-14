package proxy

import "testing"

// Review I-5 (pre-existing extractor bug): the generic fallback extracted a
// tool whose name contains "_" twice, once from the call map's "function"
// child and once more when the recursion visited that child, so one call
// completed a count-2 correlation rule on its own.
func TestExtractToolCalls_GenericFallbackExtractsEachCallOnce(t *testing.T) {
	one := []byte(`{"model":"llama3","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"/home/u/.aws/credentials"}}}]},"done":true}`)
	got := ExtractToolCallsFromResponse(one)
	if len(got) != 1 || got[0].Name != "read_file" {
		t.Fatalf("one generic call must extract once, got %+v", got)
	}

	// Two identical calls at different positions are still two calls.
	two := []byte(`{"model":"llama3","message":{"role":"assistant","content":"","tool_calls":[` +
		`{"function":{"name":"read_file","arguments":{"path":"/home/u/.aws/credentials"}}},` +
		`{"function":{"name":"read_file","arguments":{"path":"/home/u/.aws/credentials"}}}]},"done":true}`)
	if got := ExtractToolCallsFromResponse(two); len(got) != 2 {
		t.Fatalf("two identical calls at two positions are two calls, got %d", len(got))
	}

	e := credTwiceEngine(t)
	e.EvaluateToolCalls("sess-generic", proxyToolCallsToPolicyToolCalls(ExtractToolCallsFromResponse(one)))
	flushEngine(t, e)
	if e.IsFlagged("sess-generic") {
		t.Fatal("one generic call must not complete a count-2 rule")
	}
	e.EvaluateToolCalls("sess-generic2", proxyToolCallsToPolicyToolCalls(ExtractToolCallsFromResponse(two)))
	flushEngine(t, e)
	if !e.IsFlagged("sess-generic2") {
		t.Fatal("two generic calls complete a count-2 rule")
	}
}
