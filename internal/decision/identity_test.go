package decision

import "testing"

func TestCanonicalIdentity_LengthPrefixed(t *testing.T) {
	// The canonical form length-prefixes every field so no field value can
	// impersonate a field boundary.
	got := canonicalIdentity(Identity{
		SessionID:      "s",
		RequestID:      "rr",
		MessageIndex:   3,
		StartByte:      4,
		EndByte:        5,
		TransformChain: "nfkc",
		Signal:         SignalInjection,
		ModelVersion:   "m",
	})
	want := "1:s|2:rr|1:3|1:4|1:5|4:nfkc|9:injection|1:m"
	if got != want {
		t.Fatalf("canonicalIdentity:\n got %q\nwant %q", got, want)
	}
}

func TestCanonicalIdentity_EmptyFields(t *testing.T) {
	got := canonicalIdentity(Identity{})
	want := "0:|0:|1:0|1:0|1:0|0:|0:|0:"
	if got != want {
		t.Fatalf("canonicalIdentity(zero):\n got %q\nwant %q", got, want)
	}
}
