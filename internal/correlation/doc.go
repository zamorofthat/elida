// Package correlation recognizes relationships across a session's policy
// events: one category repeated within a window, or an ordered sequence of
// categories within a window.
//
// It imports nothing outside the standard library. The policy engine feeds
// it events (violation events and categorized tool calls) and turns the
// matches it returns into violations. This package never touches a risk
// score, a session or request content: an Event is an ID, a category, a
// grouping key and a timestamp, and a Match is a rule name, IDs, categories
// and timestamps.
//
// Bounds. Per session: at most Limits.MaxHistoryEvents events (shared by
// every wanted category), pruned by age at the largest rule window, at most
// MaxMatchIDsPerSession remembered match digests, and for each count_by
// message rule at most Limits.MaxHistoryEvents consumed message groups.
// Per Observe call: at most MaxEventsPerObserve events. Across sessions: at
// most Limits.MaxSessions, split across up to MaxShards independently
// locked shards, each evicting its least recently observed session first.
// Eviction loses history (a match may be missed); it never produces a
// match. DefaultMaxHistoryEvents documents the measured memory.
//
// Because each shard enforces its own share of Limits.MaxSessions with its
// own LRU, an uneven hash spread evicts in a full shard before the global
// cap is reached: at the default cap, 4096 distinct live sessions evict
// about 5% (205 measured), and live sessions start losing history at about
// 85% of MaxSessions. Size max_sessions with that headroom.
//
// Lock order. Callers do not hold their own locks across Observe: the
// policy engine collects events under its lock, releases it, calls Observe,
// then relocks, and the semantic runner's shadow correlation likewise
// observes outside its session-registry lock. This package must still never
// call back into a caller.
package correlation
