package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const (
	SDRIntegrityAlgorithm               = "sha256-merkle-v1"
	SDRCanonicalizationVersion          = "elida-sdr-canonical-v1"
	sdrLeafPrefix                       = "elida-sdr-leaf-v1:"
	sdrNodePrefix                       = "elida-sdr-node-v1:"
	sdrProofSiblingPositionLeft  string = "left"
	sdrProofSiblingPositionRight string = "right"
)

// SDRIntegrity stores the finalized Merkle root for a session detail record.
type SDRIntegrity struct {
	SessionID               string    `json:"session_id"`
	Algorithm               string    `json:"algorithm"`
	CanonicalizationVersion string    `json:"canonicalization_version"`
	EventCount              int       `json:"event_count"`
	RootHash                string    `json:"root_hash"`
	LeafHashes              []string  `json:"-"`
	CreatedAt               time.Time `json:"created_at"`
}

// MerkleProofNode is one sibling needed to verify an event inclusion proof.
type MerkleProofNode struct {
	Position string `json:"position"`
	Hash     string `json:"hash"`
}

// SDRProof proves that an event belongs to a finalized SDR Merkle root.
type SDRProof struct {
	SessionID               string            `json:"session_id"`
	EventID                 int64             `json:"event_id"`
	EventIndex              int               `json:"event_index"`
	EventHash               string            `json:"event_hash"`
	RootHash                string            `json:"root_hash"`
	SiblingPath             []MerkleProofNode `json:"sibling_path"`
	Algorithm               string            `json:"algorithm"`
	CanonicalizationVersion string            `json:"canonicalization_version"`
}

type canonicalEvent struct {
	ID        int64  `json:"id"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"event_type"`
	SessionID string `json:"session_id"`
	Severity  string `json:"severity"`
	Data      any    `json:"data"`
}

// CanonicalEventJSON returns the stable JSON representation used for SDR hashing.
// Determinism guarantee: json.Marshal sorts map keys lexicographically (Go 1.12+),
// so the output is identical regardless of the input key order in event.Data.
func CanonicalEventJSON(event Event) ([]byte, error) {
	data, err := parseEventData(event.Data)
	if err != nil {
		return nil, err
	}

	canonical := canonicalEvent{
		ID:        event.ID,
		Timestamp: event.Timestamp.UTC().Format(time.RFC3339Nano),
		Type:      string(event.Type),
		SessionID: event.SessionID,
		Severity:  event.Severity,
		Data:      data,
	}

	return json.Marshal(canonical)
}

// HashEventLeaf hashes an event as a Merkle tree leaf.
func HashEventLeaf(event Event) (string, error) {
	canonicalJSON, err := CanonicalEventJSON(event)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(sdrLeafPrefix), canonicalJSON...))
	return hex.EncodeToString(sum[:]), nil
}

// ComputeMerkleRoot returns the Merkle root for a set of leaf hashes.
func ComputeMerkleRoot(leafHashes []string) (string, error) {
	if len(leafHashes) == 0 {
		return "", fmt.Errorf("cannot compute Merkle root for zero leaves")
	}
	level := append([]string(nil), leafHashes...)
	for len(level) > 1 {
		next := make([]string, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			left := level[i]
			right := left
			if i+1 < len(level) {
				right = level[i+1]
			}
			next = append(next, hashMerkleNode(left, right))
		}
		level = next
	}
	return level[0], nil
}

// BuildMerkleProof returns the sibling path needed to prove one leaf.
func BuildMerkleProof(leafHashes []string, index int) ([]MerkleProofNode, error) {
	if len(leafHashes) == 0 {
		return nil, fmt.Errorf("cannot build proof for zero leaves")
	}
	if index < 0 || index >= len(leafHashes) {
		return nil, fmt.Errorf("event index %d out of range", index)
	}

	proof := []MerkleProofNode{}
	level := append([]string(nil), leafHashes...)
	currentIndex := index

	for len(level) > 1 {
		if currentIndex%2 == 0 {
			siblingIndex := currentIndex + 1
			siblingHash := level[currentIndex]
			if siblingIndex < len(level) {
				siblingHash = level[siblingIndex]
			}
			proof = append(proof, MerkleProofNode{
				Position: sdrProofSiblingPositionRight,
				Hash:     siblingHash,
			})
		} else {
			proof = append(proof, MerkleProofNode{
				Position: sdrProofSiblingPositionLeft,
				Hash:     level[currentIndex-1],
			})
		}

		next := make([]string, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			left := level[i]
			right := left
			if i+1 < len(level) {
				right = level[i+1]
			}
			next = append(next, hashMerkleNode(left, right))
		}
		currentIndex /= 2
		level = next
	}

	return proof, nil
}

// VerifyMerkleProof verifies a leaf hash against a root and sibling path.
func VerifyMerkleProof(eventHash, rootHash string, siblingPath []MerkleProofNode) bool {
	current := eventHash
	for _, node := range siblingPath {
		switch node.Position {
		case sdrProofSiblingPositionLeft:
			current = hashMerkleNode(node.Hash, current)
		case sdrProofSiblingPositionRight:
			current = hashMerkleNode(current, node.Hash)
		default:
			return false
		}
	}
	return current == rootHash
}

// VerifySDRProof verifies the proof root and that the supplied event index
// matches the left/right positions encoded by the sibling path.
func VerifySDRProof(proof SDRProof) bool {
	if !VerifyMerkleProof(proof.EventHash, proof.RootHash, proof.SiblingPath) {
		return false
	}
	expectedIndex := 0
	for level, node := range proof.SiblingPath {
		if node.Position == sdrProofSiblingPositionLeft {
			expectedIndex += 1 << level
		}
	}
	return expectedIndex == proof.EventIndex
}

// ComputeAndStoreSDRIntegrity finalizes and stores the Merkle root for a session.
func (s *SQLiteStore) ComputeAndStoreSDRIntegrity(ctx context.Context, sessionID string) (*SDRIntegrity, error) {
	events, err := s.getSessionEventsForIntegrity(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}

	leafHashes := make([]string, 0, len(events))
	for _, event := range events {
		leafHash, hashErr := HashEventLeaf(event)
		if hashErr != nil {
			return nil, hashErr
		}
		leafHashes = append(leafHashes, leafHash)
	}

	rootHash, err := ComputeMerkleRoot(leafHashes)
	if err != nil {
		return nil, err
	}

	integrity := SDRIntegrity{
		SessionID:               sessionID,
		Algorithm:               SDRIntegrityAlgorithm,
		CanonicalizationVersion: SDRCanonicalizationVersion,
		EventCount:              len(events),
		RootHash:                rootHash,
		LeafHashes:              leafHashes,
	}
	if err := s.SaveSDRIntegrity(ctx, integrity); err != nil {
		return nil, err
	}

	return s.GetSDRIntegrity(ctx, sessionID)
}

// SaveSDRIntegrity stores finalized SDR integrity metadata.
func (s *SQLiteStore) SaveSDRIntegrity(ctx context.Context, integrity SDRIntegrity) error {
	leafHashes, err := json.Marshal(integrity.LeafHashes)
	if err != nil {
		return fmt.Errorf("failed to marshal SDR leaf hashes: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO sdr_integrity
		(session_id, algorithm, canonicalization_version, event_count, root_hash, leaf_hashes_json)
		VALUES (?, ?, ?, ?, ?, ?)`,
		integrity.SessionID,
		integrity.Algorithm,
		integrity.CanonicalizationVersion,
		integrity.EventCount,
		integrity.RootHash,
		string(leafHashes),
	)
	if err != nil {
		return fmt.Errorf("failed to save SDR integrity metadata: %w", err)
	}
	return nil
}

// GetSDRIntegrity retrieves finalized SDR integrity metadata for a session.
func (s *SQLiteStore) GetSDRIntegrity(ctx context.Context, sessionID string) (*SDRIntegrity, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT session_id, algorithm, canonicalization_version, event_count, root_hash, leaf_hashes_json, created_at
		FROM sdr_integrity WHERE session_id = ?`, sessionID)

	var integrity SDRIntegrity
	var leafHashesJSON string
	err := row.Scan(
		&integrity.SessionID,
		&integrity.Algorithm,
		&integrity.CanonicalizationVersion,
		&integrity.EventCount,
		&integrity.RootHash,
		&leafHashesJSON,
		&integrity.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get SDR integrity metadata: %w", err)
	}

	if err := json.Unmarshal([]byte(leafHashesJSON), &integrity.LeafHashes); err != nil {
		return nil, fmt.Errorf("failed to unmarshal SDR leaf hashes: %w", err)
	}

	return &integrity, nil
}

// GetSDRProof retrieves an inclusion proof for one event in a finalized SDR.
func (s *SQLiteStore) GetSDRProof(ctx context.Context, sessionID string, eventID int64) (*SDRProof, error) {
	integrity, err := s.GetSDRIntegrity(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if integrity == nil {
		return nil, nil
	}

	events, err := s.getSessionEventsForIntegrity(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	eventIndex := -1
	var event Event
	for i, candidate := range events {
		if candidate.ID == eventID {
			eventIndex = i
			event = candidate
			break
		}
	}
	if eventIndex == -1 {
		return nil, nil
	}
	if eventIndex >= len(integrity.LeafHashes) {
		return nil, fmt.Errorf("event index %d exceeds stored SDR leaf count %d", eventIndex, len(integrity.LeafHashes))
	}

	eventHash, err := HashEventLeaf(event)
	if err != nil {
		return nil, err
	}
	if eventHash != integrity.LeafHashes[eventIndex] {
		return nil, fmt.Errorf("event %d hash mismatch: recomputed %s != stored %s (possible data corruption)", eventID, eventHash, integrity.LeafHashes[eventIndex])
	}
	siblingPath, err := BuildMerkleProof(integrity.LeafHashes, eventIndex)
	if err != nil {
		return nil, err
	}

	return &SDRProof{
		SessionID:               sessionID,
		EventID:                 eventID,
		EventIndex:              eventIndex,
		EventHash:               eventHash,
		RootHash:                integrity.RootHash,
		SiblingPath:             siblingPath,
		Algorithm:               integrity.Algorithm,
		CanonicalizationVersion: integrity.CanonicalizationVersion,
	}, nil
}

func (s *SQLiteStore) getSessionEventsForIntegrity(ctx context.Context, sessionID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, timestamp, event_type, session_id, severity, data, created_at
		FROM events WHERE session_id = ?
		ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list events for SDR integrity: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []Event
	for rows.Next() {
		var event Event
		var severity sql.NullString
		var dataStr string
		err := rows.Scan(
			&event.ID,
			&event.Timestamp,
			&event.Type,
			&event.SessionID,
			&severity,
			&dataStr,
			&event.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event for SDR integrity: %w", err)
		}
		if severity.Valid {
			event.Severity = severity.String
		}
		event.Data = json.RawMessage(dataStr)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate SDR integrity events: %w", err)
	}
	return events, nil
}

func parseEventData(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var data any
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to parse event data for canonicalization: %w", err)
	}
	return data, nil
}

func hashMerkleNode(leftHash, rightHash string) string {
	sum := sha256.Sum256([]byte(sdrNodePrefix + leftHash + rightHash))
	return hex.EncodeToString(sum[:])
}
