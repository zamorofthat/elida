// Package decision defines ELIDA's closed contract for semantic security
// signals.
//
// The contract is deliberately closed: a Provider answers a fixed set of
// Signals and advertises which it supports, rather than answering arbitrary
// natural-language questions. A fixed classifier cannot truthfully answer an
// arbitrary question, so the type system does not let callers ask one.
//
// Layering. Provider.Decide is called once per window and returns only that
// window's decisions; it knows nothing about sibling windows and nothing
// about whether the request has already been forwarded. A Scheduler splits
// content into windows, runs providers inside one global deadline, and
// composes the Assessment: Coverage and Scope are scheduler facts and are
// set nowhere else. Coverage.Complete is the authoritative verdict on whether
// a scan was clean; the Scheduler must set it equal to Coverage.IsComplete(),
// which recomputes the same thing from the counts, and a mismatch between the
// two is a bug in the Scheduler.
//
// Identifiers. Input carries no session or request ID. Those stay in the
// orchestration layer and are attached when an Assessment becomes an event,
// so an HTTP provider cannot receive internal identifiers unless a future
// contract explicitly requires and documents that disclosure.
//
// Unknown is never safe. Answered is false when a provider does not support
// a requested signal or cannot produce a result within the operation's
// constraints. Callers must check Supports during initialization and must
// never read an unanswered signal as a low probability. Assessment has two
// accessors and both report answered-ness rather than a bare number:
// MaxProbability gives one content-level value for a signal, and ByWindow
// gives the per-window decisions. The human_directed veto applies only within
// a single window, so it MUST be evaluated through ByWindow; a content-level
// maximum would let a benign window rescue an injecting one.
//
// This package imports nothing outside the standard library. That is what
// keeps the boundary honest.
package decision
