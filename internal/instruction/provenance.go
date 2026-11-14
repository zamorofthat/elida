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

	// MaxLinesPerSet bounds one version's line set. A version with more
	// distinct non-blank lines has no retained set, and a change to or from
	// it has an unknown diff: DiffKnown is false, the line counts are 0 and
	// no added lines are scanned. Such a change still grades as a warning when
	// uncaused and as no finding when caused, so a caused edit that escalates
	// inside an oversize file is not detected as an escalating change. (Its
	// rule hits still reach Static once per session, path and rule.)
	MaxLinesPerSet = 2048
	// MaxRetainedLines is the tracker-wide budget of retained line-hash
	// entries, across every chain and every global record. A set that does
	// not fit is not retained, with the same unknown-diff consequence.
	MaxRetainedLines = 1 << 21

	// maxStaticMarksPerSession bounds the (session, path|content, rule) set
	// that deduplicates Static. When it is full, hits are returned again
	// rather than forgotten: it fails toward a finding.
	maxStaticMarksPerSession = 1024
)

const (
	severityCritical = "critical"
	severityWarning  = "warning"
)

// lineSet holds salted hashes of a version's non-blank lines. A nil lineSet
// means the set is unknown (oversize, or not retained for lack of budget);
// an empty, non-nil one is a version with no non-blank lines.
type lineSet map[uint64]struct{}

// contentLines is a version's lines as computed for one request, outside the
// lock. text holds substrings of the request's content and is never retained.
type contentLines struct {
	set   lineSet  // nil when the version is oversize
	order []uint64 // distinct line hashes, first appearance first
	text  []string // the line for each entry of order
}

// chain is one session's history for one path.
type chain struct {
	shown        map[string]struct{} // content hashes this session has shown
	order        []string            // shown, oldest first, for the bound
	last         string              // the hash last added to the chain
	lines        lineSet             // retained line set of last, or nil
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
	newestLines lineSet // retained line set of newest, or nil
}

// Tracker keeps the per-session chains and the global per-path record.
// One mutex guards it, and nothing in it does I/O. Work whose size the client
// controls (matching tool arguments, hashing a file's lines, scanning added
// lines) runs outside the mutex. It holds salted hashes, counts, tool names,
// paths and content hashes; never file text or tool arguments.
type Tracker struct {
	seed    maphash.Seed
	scanner *Scanner

	mu            sync.Mutex
	maxSessions   int
	maxPaths      int
	lineBudget    int                      // MaxRetainedLines, lowered by tests
	retainedLines int                      // line-hash entries currently retained
	sessions      map[string]*list.Element // value: *sessionState
	sessionLRU    *list.List               // front = most recently observed
	paths         map[string]*pathRecord
	pathLRU       *list.List // value: *pathRecord; front = most recently observed
	now           func() time.Time

	// unlockedHook, when set by a test, runs in MarkCauses while it matches
	// calls without the lock.
	unlockedHook func()
}

func newTracker(scanner *Scanner) *Tracker {
	return &Tracker{
		seed:        maphash.MakeSeed(),
		scanner:     scanner,
		maxSessions: MaxTrackedSessions,
		maxPaths:    MaxTrackedPaths,
		lineBudget:  MaxRetainedLines,
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

func (t *Tracker) retained() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.retainedLines
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

// chainName is a chain path and its short name, as MarkCauses matches them.
type chainName struct{ path, short string }

func (n chainName) namedBy(args string) bool {
	return (n.path != "" && strings.Contains(args, n.path)) ||
		(n.short != "" && strings.Contains(args, n.short))
}

// namesTrackedBasename reports whether args name any tracked instruction file
// by its basename (CLAUDE.md, .cursor/rules, ...), whatever its directory.
func namesTrackedBasename(args string) bool {
	for _, m := range pathToFileType {
		if strings.Contains(args, strings.TrimPrefix(m.suffix, "/")) {
			return true
		}
	}
	return false
}

// causeMark is one call MarkCauses will remember: its salted identity, its
// tool name, and the chain paths it names.
type causeMark struct {
	key   uint64
	name  string
	paths []string
}

// MarkCauses remembers every new, non-read-only call that names an
// instruction file, and sets a pending cause on each chain such a call names.
//
// A call is remembered when it names one of the session's chain paths (or
// its short name) or any tracked instruction-file basename, so a call
// replayed from before the file's first sight is not new when replayed again.
// Only a match on a chain path or its short name is a cause.
//
// The session's chain paths are snapshotted under the lock, the arguments are
// matched and hashed without it, and the marks are applied under it again. A
// chain started between the snapshot and the apply is missed by these calls,
// which fails toward a finding.
func (t *Tracker) MarkCauses(sessionID string, calls []ToolUse) {
	if len(calls) == 0 {
		return
	}

	t.mu.Lock()
	var paths []string
	if el, ok := t.sessions[sessionID]; ok {
		for p := range el.Value.(*sessionState).chains {
			paths = append(paths, p)
		}
	}
	t.mu.Unlock()

	names := make([]chainName, len(paths))
	for i, p := range paths {
		names[i] = chainName{path: p, short: shortName(p)}
	}
	var marks []causeMark
	for _, c := range calls {
		if t.unlockedHook != nil {
			t.unlockedHook()
		}
		if isReadOnlyTool(c.Name) {
			continue
		}
		var matched []string
		for _, n := range names {
			if n.namedBy(c.Arguments) {
				matched = append(matched, n.path)
			}
		}
		if len(matched) == 0 && !namesTrackedBasename(c.Arguments) {
			continue
		}
		id := c.ID
		if id == "" {
			id = c.Name + "\x00" + c.Arguments
		}
		marks = append(marks, causeMark{key: maphash.String(t.seed, id), name: c.Name, paths: matched})
	}
	if len(marks) == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	var s *sessionState
	if el, ok := t.sessions[sessionID]; ok {
		s = el.Value.(*sessionState) // not moved in the LRU: only Observe is "observed"
	} else {
		s = t.touchSession(sessionID) // a call to remember before any Observe
	}
	for _, m := range marks {
		if _, seen := s.causeMarks[m.key]; seen {
			continue // a replay of an earlier request's message
		}
		if len(s.causeMarks) >= MaxCauseMarksPerSession {
			continue // full: a later change grades as uncaused
		}
		s.causeMarks[m.key] = struct{}{}
		for _, p := range m.paths {
			if ch, ok := s.chains[p]; ok {
				ch.pendingCause = m.name
			}
		}
	}
}

// retain accounts for set and returns it, or returns nil (unknown) when it is
// oversize or the budget has no room. Caller holds t.mu.
func (t *Tracker) retain(set lineSet) lineSet {
	if set == nil || len(set) > MaxLinesPerSet || t.retainedLines+len(set) > t.lineBudget {
		return nil
	}
	t.retainedLines += len(set)
	return set
}

// release returns a retained set's entries to the budget. Caller holds t.mu.
func (t *Tracker) release(set lineSet) {
	t.retainedLines -= len(set)
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
		evicted := back.Value.(*sessionState)
		for _, ch := range evicted.chains {
			t.release(ch.lines)
		}
		delete(t.sessions, evicted.id)
	}
	return s
}

// touchPath returns the global record for p, creating it and evicting the
// least recently observed path beyond the bound.
func (t *Tracker) touchPath(p string) *pathRecord {
	if pr, ok := t.paths[p]; ok {
		t.pathLRU.MoveToFront(pr.elem)
		return pr
	}
	pr := &pathRecord{path: p, versions: make(map[string]*versionRecord)}
	pr.elem = t.pathLRU.PushFront(pr)
	t.paths[p] = pr
	for len(t.paths) > t.maxPaths {
		back := t.pathLRU.Back()
		t.pathLRU.Remove(back)
		evicted := back.Value.(*pathRecord)
		t.release(evicted.newestLines)
		delete(t.paths, evicted.path)
	}
	return pr
}

// recordVersion notes that hash was seen at the path, adding it as the newest
// version when it is new.
func (t *Tracker) recordVersion(pr *pathRecord, hash string, now time.Time, set lineSet) *versionRecord {
	if v, ok := pr.versions[hash]; ok {
		v.lastSeen = now
		return v
	}
	v := &versionRecord{hash: hash, prevHash: pr.newest, firstSeen: now, lastSeen: now}
	pr.versions[hash] = v
	pr.order = append(pr.order, hash)
	pr.newest = hash
	t.release(pr.newestLines)
	pr.newestLines = t.retain(set)
	if len(pr.order) > MaxVersionsPerPath {
		delete(pr.versions, pr.order[0]) // never the newest
		pr.order = pr.order[1:]
	}
	return v
}

// addToChain makes hash the chain's last version, replacing its line set.
func (t *Tracker) addToChain(ch *chain, hash string, set lineSet) {
	ch.shown[hash] = struct{}{}
	ch.order = append(ch.order, hash)
	if len(ch.order) > MaxVersionsPerPath {
		delete(ch.shown, ch.order[0])
		ch.order = ch.order[1:]
	}
	ch.last = hash
	t.release(ch.lines)
	ch.lines = t.retain(set)
}

// linesOf hashes content's distinct non-blank lines, one trailing '\r'
// trimmed. It stops early and returns an unknown set (nil) beyond
// MaxLinesPerSet. It touches no tracker state, so it runs without the lock.
func (t *Tracker) linesOf(content string) contentLines {
	cl := contentLines{set: make(lineSet)}
	for rest := content; rest != ""; {
		var line string
		line, rest, _ = strings.Cut(rest, "\n")
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		h := maphash.String(t.seed, line)
		if _, dup := cl.set[h]; dup {
			continue
		}
		if len(cl.set) == MaxLinesPerSet {
			return contentLines{}
		}
		cl.set[h] = struct{}{}
		cl.order = append(cl.order, h)
		cl.text = append(cl.text, line)
	}
	return cl
}

// diffLines returns the text of the lines in next and not in prev, and the
// added and removed counts. Both sets are bounded by MaxLinesPerSet.
func diffLines(prev lineSet, next contentLines) (added []string, nAdded, nRemoved int) {
	for i, h := range next.order {
		if _, old := prev[h]; !old {
			added = append(added, next.text[i])
		}
	}
	for h := range prev {
		if _, kept := next.set[h]; !kept {
			nRemoved++
		}
	}
	return added, len(added), nRemoved
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
	var cl contentLines
	if file.SourcePath != "" {
		cl = t.linesOf(file.Content) // client-sized: outside the lock
	}

	t.mu.Lock()
	obs, added := t.observeLocked(sessionID, file, result, cl)
	t.mu.Unlock()

	if obs.Change != ChangeNone {
		t.grade(&obs, identified, added)
	}
	return obs
}

// observeLocked applies one observation to the session's chain and the global
// record. It returns the added lines' text (request-scoped) for grading.
// Caller holds t.mu.
func (t *Tracker) observeLocked(sessionID string, file *InstructionFile, result ScanResult, cl contentLines) (obs Observation, added []string) {
	s := t.touchSession(sessionID)

	ch, hasChain := s.chains[file.SourcePath]
	if file.SourcePath == "" || (!hasChain && len(s.chains) >= MaxPathsPerSession) {
		// No chain: only deduplicated static hits.
		obs.Static = t.markStatic(s, "h\x00"+file.Hash, result)
		return obs, nil
	}

	obs.Path, obs.Hash = file.SourcePath, file.Hash
	obs.Static = t.markStatic(s, "p\x00"+file.SourcePath, result)
	version := t.recordVersion(t.touchPath(file.SourcePath), file.Hash, t.now(), cl.set)

	if !hasChain {
		// First sight in this session: a baseline.
		ch = &chain{shown: make(map[string]struct{})}
		s.chains[file.SourcePath] = ch
		t.addToChain(ch, file.Hash, cl.set)
		return obs, nil
	}
	if _, shown := ch.shown[file.Hash]; shown {
		return obs, nil // a version this session has already shown
	}

	// A hash new to this session.
	obs.Change = ChangeInSession
	obs.PrevHash = ch.last
	if ch.lines != nil && cl.set != nil {
		obs.DiffKnown = true
		added, obs.LinesAdded, obs.LinesRemoved = diffLines(ch.lines, cl)
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
	t.addToChain(ch, file.Hash, cl.set)
	return obs, added
}

// grade scans the added lines and sets Severity, Suppressed and the final
// Static. It runs without the lock: the scanner holds no state.
func (t *Tracker) grade(obs *Observation, identified bool, added []string) {
	if len(added) > 0 && t.scanner != nil {
		hits := t.scanner.Scan(strings.Join(added, "\n"))
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

	// Rule hits inside a critical change finding are not scored again (C12).
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
}
