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
}

// Event represents an instruction integrity event for the events table.
type Event struct {
	Timestamp time.Time
	EventType string
	SessionID string
	Severity  string
	Data      map[string]interface{}
}

// registryEntry tracks the status of a known instruction file hash.
type registryEntry struct {
	status     ScanStatus
	scanResult ScanResult
}

// asyncJob is sent to the background worker for deep analysis.
type asyncJob struct {
	sessionID string
	file      InstructionFile
	result    ScanResult
}

// Registry is the in-memory hash registry with inline scanning and async persistence.
type Registry struct {
	scanner  *Scanner
	store    Store
	redactor Redactor

	mu      sync.RWMutex
	entries map[string]*registryEntry

	jobCh chan asyncJob
	done  chan struct{}
}

// NewRegistry creates a registry with the given scanner, storage backend, and async queue size.
func NewRegistry(scanner *Scanner, store Store, queueSize int) *Registry {
	if queueSize <= 0 {
		queueSize = 100
	}
	r := &Registry{
		scanner: scanner,
		store:   store,
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
func (r *Registry) Stop() {
	close(r.jobCh)
	<-r.done
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
		return entry.scanResult
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

	select {
	case r.jobCh <- asyncJob{sessionID: sessionID, file: *file, result: result}:
	default:
		slog.Warn("instruction async queue full, dropping job", "hash", truncHash(file.Hash))
	}

	return result
}

// worker processes async jobs.
func (r *Registry) worker() {
	defer close(r.done)
	for job := range r.jobCh {
		r.processJob(job)
	}
}

func (r *Registry) processJob(job asyncJob) {
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

		severity := "info"
		if status == string(ScanStatusFlagged) {
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
