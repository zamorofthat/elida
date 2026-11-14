package instruction

import (
	"log/slog"
	"sync"
	"time"
)

// Redactor redacts sensitive content before persistence.
type Redactor interface {
	Redact(content string) string
}

// Store is the storage interface the registry needs.
type Store interface {
	GetInstructionFile(hash string) (*Record, error)
	SaveInstructionFile(record Record) error
	IncrementInstructionFileSessionCount(hash string, lastSeen time.Time) error
	SaveEvent(evt Event) error
	// SavePathVersion upserts on (source_path, hash). It never moves
	// first_seen, never lowers caused from true to false, only advances
	// last_seen, and keeps prev_hash, change_type and the diff as first written.
	SavePathVersion(v PathVersion) error
	// ListPathVersions returns at most limit versions, newest last_seen first.
	ListPathVersions(limit int) ([]PathVersion, error)
}

// PathVersion is one version of one named instruction file, as seen by any
// session. It holds hashes, counts, rule and tool names and times; never file
// text, tool arguments or line hashes.
type PathVersion struct {
	SourcePath, Hash, PrevHash string
	FirstSeen, LastSeen        time.Time
	ChangeType                 string // "first_seen", "in_session", "since_last_seen"
	Caused                     bool
	CauseTool                  string
	DiffKnown                  bool
	LinesAdded, LinesRemoved   int
	AddedRuleHits              []string
}

// Version change types, as stored in PathVersion.ChangeType.
const (
	VersionFirstSeen     = "first_seen"
	VersionInSession     = string(ChangeInSession)
	VersionSinceLastSeen = string(ChangeSinceLastSeen)
)

const (
	// MaxLoadedPathVersions bounds the rows LoadPathVersions reads.
	MaxLoadedPathVersions = 50000
	// PathVersionTouchInterval is how far a version's last_seen must move
	// past its last written value before the row is written again.
	PathVersionTouchInterval = 5 * time.Minute
)

// Event represents an instruction integrity event for the events table.
type Event struct {
	Timestamp time.Time
	EventType string
	SessionID string
	Severity  string
	Data      map[string]interface{}
}

// registryEntry tracks the status of a known instruction file hash.
// scanResult is stored uncapped: entries are keyed by content hash alone, and
// the same bytes can arrive at either confidence.
type registryEntry struct {
	status     ScanStatus
	scanResult ScanResult
}

// jobKind says what an asyncJob asks the worker to write.
type jobKind int

const (
	jobInstructionFile jobKind = iota // persist a newly registered file (Check)
	jobPathVersion                    // upsert a version row (Observe)
	jobEvent                          // save a change event (Observe)
)

// asyncJob is sent to the background worker for persistence.
type asyncJob struct {
	kind      jobKind
	sessionID string
	file      InstructionFile
	result    ScanResult
	version   PathVersion
	event     Event
}

// Registry is the in-memory hash registry with inline scanning and async persistence.
type Registry struct {
	scanner  *Scanner
	store    Store
	redactor Redactor
	tracker  *Tracker

	mu      sync.RWMutex
	entries map[string]*registryEntry

	jobCh    chan asyncJob
	done     chan struct{}
	stopOnce sync.Once
}

// NewRegistry creates a registry with the given scanner, storage backend, and async queue size.
func NewRegistry(scanner *Scanner, store Store, queueSize int) *Registry {
	if queueSize <= 0 {
		queueSize = 100
	}
	r := &Registry{
		scanner: scanner,
		store:   store,
		tracker: newTracker(scanner),
		entries: make(map[string]*registryEntry),
		jobCh:   make(chan asyncJob, queueSize),
		done:    make(chan struct{}),
	}
	go r.worker()
	return r
}

// SetRedactor sets the redactor for content persistence.
func (r *Registry) SetRedactor(red Redactor) {
	r.mu.Lock()
	r.redactor = red
	r.mu.Unlock()
}

// Stop shuts down the async worker.
// It is safe to call more than once.
func (r *Registry) Stop() {
	r.stopOnce.Do(func() { close(r.jobCh) })
	<-r.done
}

// enqueue hands job to the worker without blocking; a full queue drops it.
func (r *Registry) enqueue(job asyncJob, hash string) {
	select {
	case r.jobCh <- job:
	default:
		slog.Warn("instruction async queue full, dropping job", "hash", truncHash(hash))
	}
}

// truncHash returns the first 16 characters of a hash, or the full hash if shorter.
func truncHash(hash string) string {
	if len(hash) > 16 {
		return hash[:16]
	}
	return hash
}

// Check evaluates an instruction file: cache hit → fast return, miss → inline scan + async queue.
func (r *Registry) Check(sessionID string, file *InstructionFile) ScanResult {
	if file == nil {
		return ScanResult{}
	}

	// Fast path: known hash
	r.mu.RLock()
	entry, ok := r.entries[file.Hash]
	r.mu.RUnlock()

	if ok {
		if r.store != nil {
			go func() {
				_ = r.store.IncrementInstructionFileSessionCount(file.Hash, time.Now())
			}()
		}
		slog.Debug("instruction file cache hit",
			"session_id", sessionID, "hash", truncHash(file.Hash), "status", entry.status)
		return entry.scanResult.CappedFor(file.Confidence)
	}

	// Cache miss: inline scan
	var result ScanResult
	if r.scanner != nil {
		result = r.scanner.Scan(file.Content)
	}

	status := ScanStatusClean
	if len(result.Violations) > 0 {
		status = ScanStatusFlagged
	}

	r.mu.Lock()
	r.entries[file.Hash] = &registryEntry{status: status, scanResult: result}
	r.mu.Unlock()

	slog.Info("instruction file registered",
		"session_id", sessionID,
		"hash", truncHash(file.Hash),
		"type", file.Type.String(),
		"confidence", file.Confidence,
		"status", status,
		"violations", len(result.Violations),
	)

	r.enqueue(asyncJob{kind: jobInstructionFile, sessionID: sessionID, file: *file, result: result}, file.Hash)

	return result.CappedFor(file.Confidence)
}

// worker processes async jobs.
func (r *Registry) worker() {
	defer close(r.done)
	for job := range r.jobCh {
		r.processJob(job)
	}
}

func (r *Registry) processJob(job asyncJob) {
	switch job.kind {
	case jobPathVersion:
		if r.store != nil {
			if err := r.store.SavePathVersion(job.version); err != nil {
				slog.Error("failed to persist instruction path version",
					"source_path", job.version.SourcePath, "hash", truncHash(job.version.Hash), "error", err)
			}
		}
		return
	case jobEvent:
		if r.store != nil {
			if err := r.store.SaveEvent(job.event); err != nil {
				slog.Error("failed to save instruction change event", "error", err)
			}
		}
		return
	}

	now := time.Now()
	file := job.file

	status := string(ScanStatusClean)
	if len(job.result.Violations) > 0 {
		status = string(ScanStatusFlagged)
	}

	if r.store != nil {
		existing, err := r.store.GetInstructionFile(file.Hash)
		if err != nil {
			slog.Error("failed to check existing instruction file", "error", err)
		}
		if existing != nil {
			_ = r.store.IncrementInstructionFileSessionCount(file.Hash, now)
			return
		}
	}

	r.mu.RLock()
	red := r.redactor
	r.mu.RUnlock()

	content := file.Content
	if red != nil {
		content = red.Redact(content)
	}

	record := Record{
		Hash:         file.Hash,
		FileType:     file.Type.String(),
		Confidence:   string(file.Confidence),
		SourcePath:   file.SourcePath,
		Content:      content,
		ScanStatus:   status,
		ScanResults:  job.result.Violations,
		FirstSeen:    now,
		LastSeen:     now,
		SessionCount: 1,
	}

	if r.store != nil {
		if err := r.store.SaveInstructionFile(record); err != nil {
			slog.Error("failed to persist instruction file", "hash", truncHash(file.Hash), "error", err)
			return
		}

		// A medium-confidence file was picked out by a heuristic, so its
		// ordinary rule hits are recorded without raising an alarm.
		severity := "info"
		if status == string(ScanStatusFlagged) &&
			(file.Confidence != ConfidenceMedium || job.result.hasHiddenContent()) {
			severity = "critical"
		}
		_ = r.store.SaveEvent(Event{
			Timestamp: now,
			EventType: "instruction_integrity",
			SessionID: job.sessionID,
			Severity:  severity,
			Data: map[string]interface{}{
				"hash":        file.Hash,
				"file_type":   file.Type.String(),
				"change_type": "first_seen",
				"status":      status,
				"violations":  len(job.result.Violations),
			},
		})
	}
}
