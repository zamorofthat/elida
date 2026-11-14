package instruction

import (
	"container/list"
	"hash/maphash"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
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

// The tracker's bounds. Worst case, every bound full, in entries:
//
//	sessions x shown     4096 x 1024      = 4,194,304 salted uint64s
//	sessions x marks     4096 x (512+512) = 4,194,304 salted uint64s
//	sessions x chains    4096 x 64        =   262,144 chains
//	paths x versions     4096 x 64        =   262,144 version records
//	retained line hashes                  = 2,097,152 (MaxRetainedLines)
//
// about 11M entries, a few hundred MB of maps at the very worst.
// TestWorstCaseEntries fails when a change to these passes 12M.
const (
	MaxTrackedSessions      = 4096
	MaxPathsPerSession      = 64
	MaxCauseMarksPerSession = 512
	MaxTrackedPaths         = 4096
	MaxVersionsPerPath      = 64

	// MaxShownPerSession bounds a session's shown versions across all its
	// paths. Past it the session's oldest shown entry is dropped; a dropped
	// version shown again is reported again, once, which fails toward a
	// finding.
	MaxShownPerSession = 1024

	// MaxInstructionFilesPerRequest bounds how many of a request's
	// instruction files the proxy hands to Observe. Files past it are still
	// checked (and may still refuse the request) but are not tracked.
	MaxInstructionFilesPerRequest = 32

	// MaxToolNameLen bounds a tool name as it is kept: as a pending cause,
	// a version's cause tool, an Observation's CauseTool and so in the
	// finding's description, the event and the version row.
	MaxToolNameLen = 128
	// MaxSourcePathLen bounds a path the tracker keeps. A file with a longer
	// path is handled like an unnamed block: no chain, no global record, and
	// its static hits deduplicated by content hash only.
	MaxSourcePathLen = 1024

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
	maxStaticMarksPerSession = 512
)

const (
	severityCritical = "critical"
	severityWarning  = "warning"
	severityInfo     = "info"
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
	shown        map[uint64]struct{} // salted content hashes this session has shown
	last         string              // the hash last added to the chain
	lastKey      uint64              // shownKey(last)
	lines        lineSet             // retained line set of last, or nil
	pendingCause *pendingCause       // an unconsumed cause, or nil
}

// shownRef is one shown entry, in the session's shown order.
type shownRef struct {
	ch  *chain
	key uint64
}

type sessionState struct {
	id          string
	chains      map[string]*chain
	causeMarks  map[uint64]struct{}
	staticMarks map[uint64]struct{}
	shownOrder  []shownRef // every chain's shown entries, oldest first, for the bound
	seen        identity   // set by Observe; MarkCauses alone leaves it unknown
}

// identity is whether a session was observed as identified.
type identity int8

const (
	identityUnknown identity = iota
	identityYes
	identityNo
)

// versionRecord is one version of a path as seen by any session.
type versionRecord struct {
	hash       string
	prevHash   string
	changeType string // VersionFirstSeen, VersionInSession or VersionSinceLastSeen
	firstSeen  time.Time
	lastSeen   time.Time
	written    time.Time // the lastSeen last handed out for persistence
	caused     bool
	causeTool  string
}

// row is the version as a PathVersion, with an unknown diff. Only the row
// that creates a version carries its diff; the store keeps that one.
func (v *versionRecord) row(path string) *PathVersion {
	return &PathVersion{
		SourcePath: path, Hash: v.hash, PrevHash: v.prevHash,
		FirstSeen: v.firstSeen, LastSeen: v.lastSeen, ChangeType: v.changeType,
		Caused: v.caused, CauseTool: v.causeTool,
	}
}

// versionDiff is a version's diff against the version it was compared with.
type versionDiff struct {
	known          bool
	added, removed int
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
	changes       atomic.Int64 // changes observed since start
	suppressed    atomic.Int64 // findings suppressed since start
	sinceLastSeen bool         // grade a first sight of a new version of a known path

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
//
// It writes nothing itself: a version row due a write and an event for any
// change (scored, unscored or suppressed) go to the async worker, and a full
// queue drops them.
func (r *Registry) Observe(sessionID string, identified bool, file *InstructionFile, result ScanResult) Observation {
	obs, row := r.tracker.Observe(sessionID, identified, file, result)
	if r.store == nil {
		return obs
	}
	if row != nil {
		r.enqueue(asyncJob{kind: jobPathVersion, version: *row}, row.Hash)
	}
	if obs.Change != ChangeNone {
		r.enqueue(asyncJob{kind: jobEvent, event: changeEvent(sessionID, obs)}, obs.Hash)
	}
	return obs
}

// SetChangedSinceLastSeen turns the since-last-seen finding on or off. A new
// Registry has it off; version rows are written either way.
func (r *Registry) SetChangedSinceLastSeen(on bool) {
	r.tracker.setSinceLastSeen(on)
}

// LoadPathVersions reads the persisted version record into the tracker, so a
// restart does not forget which versions of a path were seen. With no store
// it does nothing.
func (r *Registry) LoadPathVersions() error {
	if r.store == nil {
		return nil
	}
	rows, err := r.store.ListPathVersions(MaxLoadedPathVersions)
	if err != nil {
		return err
	}
	r.tracker.load(rows)
	return nil
}

// changeEvent is the instruction_file_changed event for a change observation.
// Its Data holds hashes, counts, rule and tool names and the path only.
func changeEvent(sessionID string, obs Observation) Event {
	severity := obs.Severity
	if severity == "" {
		severity = severityInfo
	}
	return Event{
		Timestamp: time.Now(),
		EventType: "instruction_file_changed",
		SessionID: sessionID,
		Severity:  severity,
		Data: map[string]interface{}{
			"source_path":     obs.Path,
			"hash":            obs.Hash,
			"prev_hash":       obs.PrevHash,
			"change_type":     string(obs.Change),
			"caused":          obs.Caused,
			"cause_tool":      obs.CauseTool,
			"diff_known":      obs.DiffKnown,
			"lines_added":     obs.LinesAdded,
			"lines_removed":   obs.LinesRemoved,
			"added_rule_hits": append([]string{}, obs.AddedRuleHits...),
			"suppressed":      obs.Suppressed,
		},
	}
}

// load adds persisted versions to the global record. rows come newest
// last_seen first. At most maxPaths paths are kept (the most recently seen),
// and per path the MaxVersionsPerPath most recently created; versions of a
// path are ordered by first_seen, so the newest is the latest created. A path
// already in memory keeps its live record. Loaded paths go to the back of the
// LRU, so loading never evicts a live path, and no line set is loaded: the
// newest version's set is filled in when a session next shows it.
func (t *Tracker) load(rows []PathVersion) {
	byPath := make(map[string][]PathVersion)
	var order []string
	for _, v := range rows {
		if v.SourcePath == "" || v.Hash == "" {
			continue
		}
		if _, ok := byPath[v.SourcePath]; !ok {
			if len(order) >= t.maxPaths {
				continue
			}
			order = append(order, v.SourcePath)
		}
		byPath[v.SourcePath] = append(byPath[v.SourcePath], v)
	}
	for _, p := range order {
		vs := byPath[p]
		sort.SliceStable(vs, func(i, j int) bool {
			if !vs[i].FirstSeen.Equal(vs[j].FirstSeen) {
				return vs[i].FirstSeen.Before(vs[j].FirstSeen)
			}
			return vs[i].LastSeen.Before(vs[j].LastSeen)
		})
		if len(vs) > MaxVersionsPerPath {
			byPath[p] = vs[len(vs)-MaxVersionsPerPath:]
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range order {
		if _, live := t.paths[p]; live || len(t.paths) >= t.maxPaths {
			continue
		}
		pr := &pathRecord{path: p, versions: make(map[string]*versionRecord)}
		for _, v := range byPath[p] {
			if _, dup := pr.versions[v.Hash]; dup {
				continue
			}
			pr.versions[v.Hash] = &versionRecord{
				hash: v.Hash, prevHash: v.PrevHash, changeType: v.ChangeType,
				firstSeen: v.FirstSeen, lastSeen: v.LastSeen, written: v.LastSeen,
				caused: v.Caused, causeTool: v.CauseTool,
			}
			pr.order = append(pr.order, v.Hash)
			pr.newest = v.Hash
		}
		pr.elem = t.pathLRU.PushBack(pr)
		t.paths[p] = pr
	}
}

// toolName is name as the tracker keeps it: at most MaxToolNameLen bytes,
// cut on a rune boundary, with control characters (below 0x20, and 0x7f)
// replaced by '?'.
func toolName(name string) string {
	if len(name) > MaxToolNameLen {
		cut := MaxToolNameLen
		for cut > 0 && !utf8.RuneStart(name[cut]) {
			cut--
		}
		name = name[:cut]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, name)
}

// tracked reports whether a file at path p gets a chain and a global record.
func tracked(p string) bool {
	return p != "" && len(p) <= MaxSourcePathLen
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

// namedBy reports whether args name the chain: by its full path where no
// path character precedes it (see fullRef), or by its short name as a
// relative reference (see relativeRef). The tail of another path
// ("/other/repo/CLAUDE.md", "/home/u/.claude/CLAUDE.md", "sub/CLAUDE.md")
// names neither way.
func (n chainName) namedBy(args string) bool {
	return (n.path != "" && fullRef(args, n.path)) ||
		(n.short != "" && relativeRef(args, n.short))
}

// pathChar reports whether b may be part of a path: an ASCII letter or digit,
// or one of _ - . / ~.
func pathChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return b == '_' || b == '-' || b == '.' || b == '/' || b == '~'
}

// fullRef reports whether p occurs in args at the start or after a byte that
// is not a pathChar. Every occurrence is tried.
func fullRef(args, p string) bool {
	for off := 0; ; {
		j := strings.Index(args[off:], p)
		if j < 0 {
			return false
		}
		i := off + j
		if i == 0 || !pathChar(args[i-1]) {
			return true
		}
		off = i + 1
	}
}

// refDelimiter reports whether b may precede a relative file reference: a
// quote, whitespace, or one of = ( [ , :.
func refDelimiter(b byte) bool {
	switch b {
	case '\'', '"', '`', ' ', '\t', '\n', '\r', '\v', '\f', '=', '(', '[', ',', ':':
		return true
	}
	return false
}

// relativeRef reports whether short occurs in args preceded by the start of
// args, a refDelimiter, or a "./" that is itself at the start or after a
// refDelimiter.
func relativeRef(args, short string) bool {
	for off := 0; ; {
		j := strings.Index(args[off:], short)
		if j < 0 {
			return false
		}
		i := off + j
		switch {
		case i == 0 || refDelimiter(args[i-1]):
			return true
		case i >= 2 && args[i-1] == '/' && args[i-2] == '.' && (i == 2 || refDelimiter(args[i-3])):
			return true
		}
		off = i + 1
	}
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

// PendingCauseTTL is how long a call that names a chain stays a possible
// cause of that chain's next change. An older pending cause is ignored.
const PendingCauseTTL = 10 * time.Minute

// pendingCause is an unconsumed cause on a chain: the arming call's tool name
// and salted identity, and when it was armed (tracker clock).
type pendingCause struct {
	tool string
	key  uint64
	at   time.Time
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
// Only a match on a chain path, or on its short name as a relative reference,
// is a cause. A pending cause expires PendingCauseTTL after it was armed, and
// is used by one change only: the change that consumes it clears the same
// call's pending cause on the session's other chains.
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
		if s := sessionOf(el); s != nil {
			for p := range s.chains {
				paths = append(paths, p)
			}
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
		marks = append(marks, causeMark{key: maphash.String(t.seed, id), name: toolName(c.Name), paths: matched})
	}
	if len(marks) == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	var s *sessionState
	if el, ok := t.sessions[sessionID]; ok {
		s = sessionOf(el) // not moved in the LRU: only Observe is "observed"
	}
	if s == nil {
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
				ch.pendingCause = &pendingCause{tool: m.name, key: m.key, at: now}
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

// sessionOf reads a session's state out of its LRU element. Only
// *sessionState values are ever stored, so nil is never returned in practice.
func sessionOf(el *list.Element) *sessionState {
	s, ok := el.Value.(*sessionState)
	if !ok {
		return nil
	}
	return s
}

// touchSession returns the session's state, creating it and evicting the
// least recently observed session beyond the bound.
func (t *Tracker) touchSession(id string) *sessionState {
	if el, ok := t.sessions[id]; ok {
		if s := sessionOf(el); s != nil {
			t.sessionLRU.MoveToFront(el)
			return s
		}
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
		evicted := sessionOf(back)
		if evicted == nil {
			break // unreachable: only *sessionState is stored
		}
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
		evicted, ok := back.Value.(*pathRecord)
		if !ok {
			break // unreachable: only *pathRecord is stored
		}
		t.release(evicted.newestLines)
		delete(t.paths, evicted.path)
	}
	return pr
}

// setSinceLastSeen turns the since-last-seen finding on or off.
func (t *Tracker) setSinceLastSeen(on bool) {
	t.mu.Lock()
	t.sinceLastSeen = on
	t.mu.Unlock()
}

// seeVersion advances a known version's lastSeen and reports whether its row
// is due a write: lastSeen has moved by more than PathVersionTouchInterval
// since it was last handed out.
func seeVersion(v *versionRecord, now time.Time) bool {
	v.lastSeen = now
	if v.lastSeen.Sub(v.written) > PathVersionTouchInterval {
		v.written = v.lastSeen
		return true
	}
	return false
}

// newVersion adds hash as the path's newest version, with set as its line
// set. The caller has checked that hash is not a version of the path.
func (t *Tracker) newVersion(pr *pathRecord, hash, prevHash, changeType string, now time.Time, set lineSet) *versionRecord {
	v := &versionRecord{hash: hash, prevHash: prevHash, changeType: changeType,
		firstSeen: now, lastSeen: now, written: now}
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

// shownKey is the salted key a chain's shown set holds for a content hash.
func (t *Tracker) shownKey(hash string) uint64 {
	return maphash.String(t.seed, hash)
}

// addToChain makes hash the chain's last version, replacing its line set.
// hash is not in the chain's shown set. Past MaxShownPerSession the
// session's oldest shown entry, of whichever chain, is dropped.
func (t *Tracker) addToChain(s *sessionState, ch *chain, hash string, set lineSet) {
	key := t.shownKey(hash)
	ch.shown[key] = struct{}{}
	s.shownOrder = append(s.shownOrder, shownRef{ch: ch, key: key})
	ch.last, ch.lastKey = hash, key
	t.evictShown(s, MaxShownPerSession)
	t.release(ch.lines)
	ch.lines = t.retain(set)
}

// evictShown drops the session's oldest shown entries until at most max
// remain. A chain's current version is never dropped: it moves to the back
// instead, so an unchanged file shown again is not a change. When every
// entry left is a current version, eviction stops (there are at most
// MaxPathsPerSession of them). Caller holds t.mu.
func (t *Tracker) evictShown(s *sessionState, max int) {
	for kept := 0; len(s.shownOrder) > max && kept < len(s.shownOrder); {
		ref := s.shownOrder[0]
		s.shownOrder[0] = shownRef{}
		s.shownOrder = s.shownOrder[1:]
		if ref.key == ref.ch.lastKey {
			s.shownOrder = append(s.shownOrder, ref)
			kept++
			continue
		}
		delete(ref.ch.shown, ref.key)
		kept = 0
	}
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

// observed is what observeLocked hands back to Observe.
type observed struct {
	obs     Observation
	added   []string     // added lines' text (request-scoped), for the scan
	row     *PathVersion // the version row due a write, or nil
	rowHits bool         // row's AddedRuleHits are the scan of added
}

// Observe implements Registry.Observe. It also returns the version row the
// observation made due for persistence, or nil; it does no I/O itself.
func (t *Tracker) Observe(sessionID string, identified bool, file *InstructionFile, result ScanResult) (Observation, *PathVersion) {
	if file == nil {
		return Observation{}, nil
	}
	var cl contentLines
	if tracked(file.SourcePath) {
		cl = t.linesOf(file.Content) // client-sized: outside the lock
	}

	t.mu.Lock()
	o := t.observeLocked(sessionID, identified, file, result, cl)
	if el, ok := t.sessions[sessionID]; ok {
		if s := sessionOf(el); s != nil {
			if identified {
				s.seen = identityYes
			} else {
				s.seen = identityNo
			}
		}
	}
	t.mu.Unlock()

	hits := t.ruleHits(o.added) // outside the lock: the scanner holds no state
	if o.obs.Change != ChangeNone {
		o.obs.AddedRuleHits = hits
		grade(&o.obs, identified)
		t.changes.Add(1)
		if o.obs.Suppressed {
			t.suppressed.Add(1)
		}
	}
	if o.row != nil && o.rowHits && len(hits) > 0 {
		o.row.AddedRuleHits = append([]string(nil), hits...)
	}
	return o.obs, o.row
}

// observeLocked applies one observation to the session's chain and the global
// record. Caller holds t.mu. Only an identified session's cause marks the
// global version caused: an unidentified session's cause is its own.
func (t *Tracker) observeLocked(sessionID string, identified bool, file *InstructionFile, result ScanResult, cl contentLines) (o observed) {
	s := t.touchSession(sessionID)

	ch, hasChain := s.chains[file.SourcePath]
	if !tracked(file.SourcePath) || (!hasChain && len(s.chains) >= MaxPathsPerSession) {
		// No chain: only deduplicated static hits.
		o.obs.Static = t.markStatic(s, "h\x00"+file.Hash, result)
		return o
	}

	o.obs.Path, o.obs.Hash = file.SourcePath, file.Hash
	o.obs.Static = t.markStatic(s, "p\x00"+file.SourcePath, result)

	now := t.now()
	_, pathKnown := t.paths[file.SourcePath]
	pr := t.touchPath(file.SourcePath)
	version, versionKnown := pr.versions[file.Hash]

	if !hasChain {
		// First sight in this session. The session's chain starts here.
		ch = &chain{shown: make(map[uint64]struct{})}
		s.chains[file.SourcePath] = ch
		t.firstSight(&o, pr, pathKnown, version, file.Hash, now, cl)
		t.addToChain(s, ch, file.Hash, cl.set)
		return o
	}

	if _, shown := ch.shown[t.shownKey(file.Hash)]; shown || file.Hash == ch.last {
		// A version this session has already shown: no finding.
		if versionKnown {
			if seeVersion(version, now) {
				o.row = version.row(file.SourcePath)
			}
		} else {
			// The global record lost it (evicted or past the version bound).
			changeType, prev := VersionFirstSeen, ""
			if pathKnown {
				changeType, prev = VersionSinceLastSeen, pr.newest
			}
			version = t.newVersion(pr, file.Hash, prev, changeType, now, cl.set)
			o.row = version.row(file.SourcePath)
		}
		return o
	}

	// A hash new to this session.
	o.obs.Change = ChangeInSession
	o.obs.PrevHash = ch.last
	if ch.lines != nil && cl.set != nil {
		o.obs.DiffKnown = true
		o.added, o.obs.LinesAdded, o.obs.LinesRemoved = diffLines(ch.lines, cl)
	}

	cause := t.consumeCause(s, ch, now)
	if versionKnown {
		due := seeVersion(version, now)
		if cause != "" && identified && !version.caused {
			version.caused, version.causeTool = true, cause
			due = true
		}
		if due {
			o.row = version.row(file.SourcePath)
		}
	} else {
		// The session's change creates the version: its row carries this diff.
		version = t.newVersion(pr, file.Hash, ch.last, VersionInSession, now, cl.set)
		if cause != "" && identified {
			version.caused, version.causeTool = true, cause
		}
		o.row = version.row(file.SourcePath)
		o.row.DiffKnown, o.row.LinesAdded, o.row.LinesRemoved = o.obs.DiffKnown, o.obs.LinesAdded, o.obs.LinesRemoved
		o.rowHits = true
	}
	if cause != "" {
		o.obs.Caused, o.obs.CauseTool = true, cause
	} else if version.caused {
		o.obs.Caused, o.obs.CauseTool = true, version.causeTool
	}
	t.addToChain(s, ch, file.Hash, cl.set)
	return o
}

// consumeCause takes the chain's pending cause and returns its tool name, or
// "" when there is none or it has expired. A cause taken also clears the same
// call's pending cause on the session's other chains: one call is one cause.
// Caller holds t.mu.
func (t *Tracker) consumeCause(s *sessionState, ch *chain, now time.Time) string {
	pc := ch.pendingCause
	ch.pendingCause = nil
	if pc == nil || now.Sub(pc.at) > PendingCauseTTL {
		return ""
	}
	for _, other := range s.chains {
		if other.pendingCause != nil && other.pendingCause.key == pc.key {
			other.pendingCause = nil
		}
	}
	return pc.tool
}

// firstSight handles a session's first sight of a path against the global
// record. Caller holds t.mu.
//
//  1. Path unknown: a baseline; the version is created as first_seen.
//  2. Path known, hash one of its versions: a baseline; lastSeen advances, and
//     the newest version's missing line set (after a restart) is filled in.
//  3. Path known, hash new: the version is created as since_last_seen against
//     the path's newest version. With the toggle on it is a finding, never
//     caused; the diff is known only when the newest line set is in memory.
func (t *Tracker) firstSight(o *observed, pr *pathRecord, pathKnown bool, version *versionRecord, hash string, now time.Time, cl contentLines) {
	switch {
	case !pathKnown:
		version = t.newVersion(pr, hash, "", VersionFirstSeen, now, cl.set)
		o.row = version.row(pr.path)

	case version != nil:
		if hash == pr.newest && pr.newestLines == nil {
			pr.newestLines = t.retain(cl.set)
		}
		if seeVersion(version, now) {
			o.row = version.row(pr.path)
		}

	default:
		prev := pr.newest
		var d versionDiff
		var added []string
		if pr.newestLines != nil && cl.set != nil {
			d.known = true
			added, d.added, d.removed = diffLines(pr.newestLines, cl)
		}
		version = t.newVersion(pr, hash, prev, VersionSinceLastSeen, now, cl.set)
		o.row = version.row(pr.path)
		o.row.DiffKnown, o.row.LinesAdded, o.row.LinesRemoved = d.known, d.added, d.removed
		o.added, o.rowHits = added, true
		if t.sinceLastSeen {
			o.obs.Change = ChangeSinceLastSeen
			o.obs.PrevHash = prev
			o.obs.DiffKnown, o.obs.LinesAdded, o.obs.LinesRemoved = d.known, d.added, d.removed
		}
	}
}

// ruleHits scans the added lines and returns the sorted, distinct names of
// the rules they trip. It runs without the lock: the scanner holds no state.
func (t *Tracker) ruleHits(added []string) []string {
	if len(added) == 0 || t.scanner == nil {
		return nil
	}
	hits := t.scanner.Scan(strings.Join(added, "\n"))
	var names []string
	seen := make(map[string]bool, len(hits.Violations))
	for _, v := range hits.Violations {
		if !seen[v.RuleName] {
			seen[v.RuleName] = true
			names = append(names, v.RuleName)
		}
	}
	sort.Strings(names)
	return names
}

// grade sets Severity, Suppressed and the final Static from the observation's
// change and AddedRuleHits.
func grade(obs *Observation, identified bool) {
	switch {
	case len(obs.AddedRuleHits) > 0:
		obs.Severity = severityCritical
	case obs.Change == ChangeSinceLastSeen:
		obs.Severity = severityInfo // never caused; an unknown diff is info too
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

// Stats is the operator's view of instruction provenance tracking.
// A session counts toward IdentifiedSessions or FallbackSessions once Observe
// has run for it; a session that so far only had MarkCauses called is
// identified as unknown and is counted in neither.
type Stats struct {
	IdentifiedSessions   int   `json:"identified_sessions"` // currently tracked
	FallbackSessions     int   `json:"fallback_sessions"`   // currently tracked
	TrackedPaths         int   `json:"tracked_paths"`
	Changes              int64 `json:"changes"`             // since start
	SuppressedFindings   int64 `json:"suppressed_findings"` // since start
	ChangedSinceLastSeen bool  `json:"changed_since_last_seen"`
}

// Stats returns the current counters. It holds only counts.
func (r *Registry) Stats() Stats {
	t := r.tracker
	t.mu.Lock()
	st := Stats{TrackedPaths: len(t.paths), ChangedSinceLastSeen: t.sinceLastSeen}
	for _, el := range t.sessions {
		s := sessionOf(el)
		if s == nil {
			continue
		}
		switch s.seen {
		case identityYes:
			st.IdentifiedSessions++
		case identityNo:
			st.FallbackSessions++
		}
	}
	t.mu.Unlock()
	st.Changes, st.SuppressedFindings = t.changes.Load(), t.suppressed.Load()
	return st
}
