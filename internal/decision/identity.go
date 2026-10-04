package decision

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// Identity is the tuple a stable decision ID is derived from: session,
// request, message index, original byte range, transformation chain, signal
// and model version.
//
// Two decisions with the same Identity are the same decision and contribute
// risk once, no matter how many times they arrive — a retry, an overlapping
// window, the same bytes reached through two chains, or the same job
// completing inline and then async.
type Identity struct {
	SessionID      string
	RequestID      string
	MessageIndex   int
	StartByte      int
	EndByte        int
	TransformChain string
	Signal         Signal
	ModelVersion   string
}

// WithWindow returns a copy of id with the window's byte range and
// transformation chain filled in. The receiver is not modified.
func (id Identity) WithWindow(w Window) Identity {
	id.StartByte = w.StartByte
	id.EndByte = w.EndByte
	id.TransformChain = w.Transform
	return id
}

// JobIdentity is the dedup key for one unit of scheduler work: one window of
// one message of one request.
//
// It deliberately omits signal and model version, because one job scores
// every requested signal for that window with one model. That is what makes
// an inline completion and a later async completion of the same work
// recognizable as duplicates.
type JobIdentity struct {
	SessionID      string
	RequestID      string
	MessageIndex   int
	StartByte      int
	EndByte        int
	TransformChain string
}

// canonicalIdentity renders an Identity as an unambiguous string.
//
// Every field is length-prefixed ("<len>:<value>") and joined with "|", so
// no field value can impersonate a field boundary: ("sess", "abc-req-1")
// and ("sess-abc", "req-1") produce different strings.
func canonicalIdentity(id Identity) string {
	var b strings.Builder
	fields := []string{
		id.SessionID,
		id.RequestID,
		strconv.Itoa(id.MessageIndex),
		strconv.Itoa(id.StartByte),
		strconv.Itoa(id.EndByte),
		id.TransformChain,
		string(id.Signal),
		id.ModelVersion,
	}
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return b.String()
}

// canonicalJob renders a JobIdentity the same way canonicalIdentity does.
func canonicalJob(j JobIdentity) string {
	return canonicalIdentity(Identity{
		SessionID:      j.SessionID,
		RequestID:      j.RequestID,
		MessageIndex:   j.MessageIndex,
		StartByte:      j.StartByte,
		EndByte:        j.EndByte,
		TransformChain: j.TransformChain,
	})
}

// DecisionID returns the stable ID for one decision: "dec_" followed by the
// first 16 bytes of the SHA-256 of the canonical identity, hex-encoded.
//
// 128 bits is ample: these IDs deduplicate within one session's bounded
// event history, they are not secrets, and they are not an authentication
// token.
func DecisionID(id Identity) string {
	sum := sha256.Sum256([]byte("elida.decision.v1\x00" + canonicalIdentity(id)))
	return "dec_" + hex.EncodeToString(sum[:16])
}

// JobID returns the stable ID for one unit of scheduler work. The domain
// prefix differs from DecisionID's, so a job ID and a decision ID can never
// collide even over identical fields.
func JobID(j JobIdentity) string {
	sum := sha256.Sum256([]byte("elida.job.v1\x00" + canonicalJob(j)))
	return "job_" + hex.EncodeToString(sum[:16])
}
