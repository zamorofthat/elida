//go:build !race

package unit

// raceEnabled reports whether this test binary runs the race detector,
// which slows everything by an order of magnitude. Wall-clock sanity guards
// are skipped under it; the invariants they back are asserted structurally.
const raceEnabled = false
