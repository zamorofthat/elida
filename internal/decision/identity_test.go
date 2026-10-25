package decision

import "testing"

func TestCanonicalIdentity_LengthPrefixed(t *testing.T) {
	// The canonical form opens with its version and length-prefixes every
	// field so no field value can impersonate a field boundary.
	got := canonicalIdentity(Identity{
		SessionID:      "s",
		RequestID:      "rr",
		MessageIndex:   3,
		SourceRole:     "tool",
		StartByte:      4,
		EndByte:        5,
		LocalStartByte: 6,
		LocalEndByte:   78,
		TransformChain: "nfkc",
		Signal:         SignalInjection,
		ModelVersion:   "m",
	})
	want := "v3|1:s|2:rr|1:3|4:tool|1:4|1:5|1:6|2:78|4:nfkc|9:injection|1:m"
	if got != want {
		t.Fatalf("canonicalIdentity:\n got %q\nwant %q", got, want)
	}
}

func TestCanonicalIdentity_EmptyFields(t *testing.T) {
	got := canonicalIdentity(Identity{})
	want := "v3|0:|0:|1:0|0:|1:0|1:0|1:0|1:0|0:|0:|0:"
	if got != want {
		t.Fatalf("canonicalIdentity(zero):\n got %q\nwant %q", got, want)
	}
}

func TestCanonicalJob_CarriesSourceRole(t *testing.T) {
	got := canonicalJob(JobIdentity{SessionID: "s", RequestID: "r", MessageIndex: 2, SourceRole: "user", EndByte: 20, LocalEndByte: 20})
	want := "v3|1:s|1:r|1:2|4:user|1:0|2:20|1:0|2:20|0:|0:|0:"
	if got != want {
		t.Fatalf("canonicalJob:\n got %q\nwant %q", got, want)
	}
}
