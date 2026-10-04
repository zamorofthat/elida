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
// set nowhere else.
//
// Identifiers. Input carries no session or request ID. Those stay in the
// orchestration layer and are attached when an Assessment becomes an event,
// so an HTTP provider cannot receive internal identifiers unless a future
// contract explicitly requires and documents that disclosure.
//
// Unknown is never safe. Answered is false when a provider does not support
// a requested signal or cannot produce a result within the operation's
// constraints. Callers must check Supports during initialization and must
// never read an unanswered signal as a low probability; Assessment.MaxProbability
// is the only supported way to read a signal and it reports answered
// separately from the value.
//
// This package imports nothing outside the standard library. That is what
// keeps the boundary honest.
package decision
