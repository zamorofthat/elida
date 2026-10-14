package proxy

import "testing"

// The tool-call ID is what lets correlation identify one provider call, so
// the converter must carry it.
func TestProxyToolCallsToPolicyToolCalls_CarriesID(t *testing.T) {
	got := proxyToolCallsToPolicyToolCalls([]ToolCallInfo{
		{Name: "read_file", Type: "function", ID: "call_1", Arguments: `{"path":"x"}`},
	})
	if len(got) != 1 || got[0].ID != "call_1" || got[0].Name != "read_file" || got[0].Arguments != `{"path":"x"}` {
		t.Fatalf("converter dropped a field: %+v", got)
	}
}
