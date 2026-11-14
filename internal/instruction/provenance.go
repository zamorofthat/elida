package instruction

import (
	"container/list"
	"hash/maphash"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// ToolUse is a tool call found in a request's message history.
type ToolUse struct{ ID, Name, Arguments string }

// ChangeKind says what kind of change an observation found.
type ChangeKind string

const (
	ChangeNone          ChangeKind = ""
	ChangeInSession     ChangeKind = "in_session"
	ChangeSinceLastSeen ChangeKind = "since_last_seen" // produced from Task 6 on
)

const (
	RuleFileChanged              = "instruction_file_changed"
	RuleFileChangedSinceLastSeen = "instruction_file_changed_since_last_seen"
	CategoryProvenance           = "instruction_provenance" // plan 4's chain rule keys on this
)

// Observation is what Observe found for one instruction file in one request.
// It never carries file text or tool arguments.
type Observation struct {
	Change        ChangeKind
	Path, Hash    string
	PrevHash      string
	Caused        bool
	CauseTool     string // a tool name, never its arguments
	DiffKnown     bool
	LinesAdded    int
	LinesRemoved  int
	AddedRuleHits []string    // sorted names of the rules the added lines trip
	Severity      string      // "critical", "warning", "info"; "" means no finding
	Suppressed    bool        // a finding was due and the session is not identified
	Static        []Violation // rule hits to score once, at system weight
}

// ReadOnlyToolGlobs are matched (path.Match) against a lower-cased tool name;
// a matching call never counts as the cause of a change.
var ReadOnlyToolGlobs = []string{"read", "grep", "glob", "ls", "notebookread",
	"read_*", "*_read", "*__read*", "*__list*", "*__search*", "*__get*"}

const (
	MaxTrackedSessions      = 4096
	MaxPathsPerSession      = 64
	MaxCauseMarksPerSession = 1024
	MaxTrackedPaths         = 8192
	MaxVersionsPerPath      = 256

	// maxStaticMarksPerSession bounds the (session, path|content, rule) set
	// that deduplicates Static. When it is full, hits are returned again
	// rather than forgotten: it fails toward a finding.
	maxStaticMarksPerSession = 1024
)

const (
	severityCritical = "critical"
	severityWarning  = "warning"
)

// lineSet holds salted hashes of a version's non-blank lines.
type lineSet map[uint64]struct{}

// chain is one session's history for one path.
type chain struct {
	shown        map[string]struct{} // content hashes this session has shown
	order        []string            // shown, oldest first, for the bound
	last         string              // the hash last added to the chain
	lines        lineSet             // line set of last
	pendingCause string              // tool name of an unconsumed cause mark
}

type sessionState struct {
	id          string
	chains      map[string]*chain
	causeMarks  map[uint64]struct{}
	staticMarks map[uint64]struct{}
}

// versionRecord is one version of a path as seen by any session.
type versionRecord struct {
	hash      string
	prevHash  string
	firstSeen time.Time
	lastSeen  time.Time
	caused    bool
	causeTool string
}

// pathRecord is the global, cross-session record for one path.
type pathRecord struct {
	path        string
	elem        *list.Element
	versions    map[string]*versionRecord
	order       []string // version hashes, oldest first, for the bound
	newest      string
	newestLines lineSet
}

// Tracker keeps the per-session chains and the global per-path record.
// One mutex guards it, and nothing in it does I/O. It holds salted hashes,
// counts, tool names, paths and content hashes; never file text or tool
// arguments.
type Tracker struct {
	seed    maphash.Seed
	scanner *Scanner

	mu          sync.Mutex
	maxSessions int
	sessions    map[string]*list.Element // value: *sessionState
	sessionLRU  *list.List               // front = most recently observed
	paths       map[string]*pathRecord
	pathLRU     *list.List // value: *pathRecord; front = most recently observed
	now         func() time.Time
}

func newTracker(scanner *Scanner) *Tracker {
	return &Tracker{
		seed:        maphash.MakeSeed(),
		scanner:     scanner,
		maxSessions: MaxTrackedSessions,
		sessions:    make(map[string]*list.Element),
		sessionLRU:  list.New(),
		paths:       make(map[string]*pathRecord),
		pathLRU:     list.New(),
		now:         time.Now,
	}
}

func (t *Tracker) sessionCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.sessions)
}

// MarkCauses records which instruction files the given tool calls name.
func (r *Registry) MarkCauses(sessionID string, calls []ToolUse) {
	r.tracker.MarkCauses(sessionID, calls)
}

// Observe updates the session's chain for the file and grades any change.
// Call it for every extracted file on every request, after Check.
func (r *Registry) Observe(sessionID string, identified bool, file *InstructionFile, result ScanResult) Observation {
	return r.tracker.Observe(sessionID, identified, file, result)
}

func isReadOnlyTool(name string) bool {
	lower := strings.ToLower(name)
	for _, g := range ReadOnlyToolGlobs {
		if ok, _ := path.Match(g, lower); ok {
			return true
		}
	}
	return false
}

// shortName is the basename of p, or its last two elements when the basename
// has no '.' in it (".cursor/rules", not "rules").
func shortName(p string) string {
	p = strings.TrimRight(p, "/")
	i := strings.LastIndex(p, "/")
	base := p[i+1:]
	if strings.Contains(base, ".") || i <= 0 {
		return base
	}
	j := strings.LastIndex(p[:i], "/")
	return p[j+1:]
}

func namesPath(args, p string) bool {
	if p != "" && strings.Contains(args, p) {
		return true
	}
	s := shortName(p)
	return s != "" && strings.Contains(args, s)
}

// MarkCauses sets a pending cause on each chain a new, non-read-only call names.
func (t *Tracker) MarkCauses(sessionID string, calls []ToolUse) {
	if len(calls) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	el, ok := t.sessions[sessionID]
	if !ok {
		return
	}
	s := el.Value.(*sessionState)
	var matched []*chain
	for _, c := range calls {
		if isReadOnlyTool(c.Name) {
			continue
		}
		matched = matched[:0]
		for p, ch := range s.chains {
			if namesPath(c.Arguments, p) {
				matched = append(matched, ch)
			}
		}
		if len(matched) == 0 {
			continue
		}
		id := c.ID
		if id == "" {
			id = c.Name + "\x00" + c.Arguments
		}
		key := maphash.String(t.seed, id)
		if _, seen := s.causeMarks[key]; seen {
			continue // a replay of an earlier request's message
		}
		if len(s.causeMarks) >= MaxCauseMarksPerSession {
			continue // full: a later change grades as uncaused
		}
		s.causeMarks[key] = struct{}{}
		for _, ch := range matched {
			ch.pendingCause = c.Name
		}
	}
}

// touchSession returns the session's state, creating it and evicting the
// least recently observed session beyond the bound.
func (t *Tracker) touchSession(id string) *sessionState {
	if el, ok := t.sessions[id]; ok {
		t.sessionLRU.MoveToFront(el)
		return el.Value.(*sessionState)
	}
	s := &sessionState{
		id:          id,
		chains:      make(map[string]*chain),
		causeMarks:  make(map[uint64]struct{}),
		staticMarks: make(map[uint64]struct{}),
	}
	t.sessions[id] = t.sessionLRU.PushFront(s)
	for len(t.sessions) > t.maxSessions {
		back := t.sessionLRU.Back()
		t.sessionLRU.Remove(back)
		delete(t.sessions, back.Value.(*sessionState).id)
	}
	return s
}

// touchPath returns the global record for p, creating it and evicting the
// least recently observed path beyond MaxTrackedPaths.
func (t *Tracker) touchPath(p string) *pathRecord {
	if pr, ok := t.paths[p]; ok {
		t.pathLRU.MoveToFront(pr.elem)
		return pr
	}
	pr := &pathRecord{path: p, versions: make(map[string]*versionRecord)}
	pr.elem = t.pathLRU.PushFront(pr)
	t.paths[p] = pr
	for len(t.paths) > MaxTrackedPaths {
		back := t.pathLRU.Back()
		t.pathLRU.Remove(back)
		delete(t.paths, back.Value.(*pathRecord).path)
	}
	return pr
}

// recordVersion notes that hash was seen at path, adding it as the newest
// version when it is new. lines is computed on demand.
func (pr *pathRecord) recordVersion(hash string, now time.Time, lines func() lineSet) *versionRecord {
	if v, ok := pr.versions[hash]; ok {
		v.lastSeen = now
		return v
	}
	v := &versionRecord{hash: hash, prevHash: pr.newest, firstSeen: now, lastSeen: now}
	pr.versions[hash] = v
	pr.order = append(pr.order, hash)
	pr.newest = hash
	pr.newestLines = lines()
	if len(pr.order) > MaxVersionsPerPath {
		delete(pr.versions, pr.order[0])
		pr.order = pr.order[1:]
	}
	return v
}

func (ch *chain) add(hash string, lines lineSet) {
	ch.shown[hash] = struct{}{}
	ch.order = append(ch.order, hash)
	if len(ch.order) > MaxVersionsPerPath {
		delete(ch.shown, ch.order[0])
		ch.order = ch.order[1:]
	}
	ch.last = hash
	ch.lines = lines
}

// eachLine calls fn for every non-blank line, with one trailing '\r' trimmed.
func eachLine(content string, fn func(line string)) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fn(line)
	}
}

func (t *Tracker) lineSetOf(content string) lineSet {
	set := make(lineSet)
	eachLine(content, func(line string) { set[maphash.String(t.seed, line)] = struct{}{} })
	return set
}

// markStatic returns the violations not returned before under the given key
// prefix for this session, and marks them returned.
func (t *Tracker) markStatic(s *sessionState, prefix string, result ScanResult) []Violation {
	var out []Violation
	for _, v := range result.Violations {
		key := maphash.String(t.seed, prefix+"\x00"+v.RuleName)
		if _, done := s.staticMarks[key]; done {
			continue
		}
		if len(s.staticMarks) < maxStaticMarksPerSession {
			s.staticMarks[key] = struct{}{}
		}
		out = append(out, v)
	}
	return out
}

// Observe implements Registry.Observe.
func (t *Tracker) Observe(sessionID string, identified bool, file *InstructionFile, result ScanResult) Observation {
	if file == nil {
		return Observation{}
	}

	var (
		obs        Observation
		addedLines []string
	)
	func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		now := t.now()
		s := t.touchSession(sessionID)

		ch, hasChain := s.chains[file.SourcePath]
		if file.SourcePath == "" || (!hasChain && len(s.chains) >= MaxPathsPerSession) {
			// No chain: only deduplicated static hits.
			obs.Static = t.markStatic(s, "h\x00"+file.Hash, result)
			return
		}

		obs.Path, obs.Hash = file.SourcePath, file.Hash
		obs.Static = t.markStatic(s, "p\x00"+file.SourcePath, result)

		var newLines lineSet
		linesOf := func() lineSet {
			if newLines == nil {
				newLines = t.lineSetOf(file.Content)
			}
			return newLines
		}
		pr := t.touchPath(file.SourcePath)
		version := pr.recordVersion(file.Hash, now, linesOf)

		if !hasChain {
			// First sight in this session: a baseline.
			ch = &chain{shown: make(map[string]struct{})}
			s.chains[file.SourcePath] = ch
			ch.add(file.Hash, linesOf())
			return
		}
		if _, shown := ch.shown[file.Hash]; shown {
			return // a version this session has already shown
		}

		// A hash new to this session.
		obs.Change = ChangeInSession
		obs.PrevHash = ch.last
		obs.DiffKnown = true
		next := linesOf()
		added := make(lineSet)
		eachLine(file.Content, func(line string) {
			h := maphash.String(t.seed, line)
			if _, old := ch.lines[h]; old {
				return
			}
			if _, dup := added[h]; dup {
				return
			}
			added[h] = struct{}{}
			addedLines = append(addedLines, line)
		})
		obs.LinesAdded = len(added)
		for h := range ch.lines {
			if _, kept := next[h]; !kept {
				obs.LinesRemoved++
			}
		}

		if ch.pendingCause != "" {
			obs.Caused, obs.CauseTool = true, ch.pendingCause
			ch.pendingCause = ""
			if !version.caused {
				version.caused, version.causeTool = true, obs.CauseTool
			}
		} else if version.caused {
			obs.Caused, obs.CauseTool = true, version.causeTool
		}
		ch.add(file.Hash, next)
	}()

	if obs.Change == ChangeNone {
		return obs
	}

	// Scan the added lines outside the lock: the scanner holds no state.
	if len(addedLines) > 0 && t.scanner != nil {
		hits := t.scanner.Scan(strings.Join(addedLines, "\n"))
		seen := make(map[string]bool, len(hits.Violations))
		for _, v := range hits.Violations {
			if !seen[v.RuleName] {
				seen[v.RuleName] = true
				obs.AddedRuleHits = append(obs.AddedRuleHits, v.RuleName)
			}
		}
		sort.Strings(obs.AddedRuleHits)
	}

	switch {
	case len(obs.AddedRuleHits) > 0:
		obs.Severity = severityCritical
	case !obs.Caused:
		obs.Severity = severityWarning
	}
	if obs.Severity != "" && !identified {
		obs.Suppressed, obs.Severity = true, ""
	}

	if obs.Severity == severityCritical && len(obs.Static) > 0 {
		inFinding := make(map[string]bool, len(obs.AddedRuleHits))
		for _, n := range obs.AddedRuleHits {
			inFinding[n] = true
		}
		kept := obs.Static[:0:0]
		for _, v := range obs.Static {
			if !inFinding[v.RuleName] {
				kept = append(kept, v)
			}
		}
		obs.Static = kept
	}
	return obs
}
