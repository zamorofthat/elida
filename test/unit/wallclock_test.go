package unit

import (
	"testing"
	"time"
)

// wallClockGuard is the generous bound of a wall-clock sanity guard. It
// catches hangs and gross regressions; it is never a performance budget.
// The invariants are what the tests assert directly: exact-counter call
// bounds, admission reasons, answeredness and coverage gaps.
const wallClockGuard = 5 * time.Second

// checkWallClock logs how long an operation took and fails only past
// wallClockGuard, and never under the race detector, whose slowdown on a
// loaded CI runner makes any wall-clock bound flaky.
func checkWallClock(t *testing.T, what string, elapsed time.Duration) {
	t.Helper()
	t.Logf("%s took %v", what, elapsed)
	if !raceEnabled && elapsed > wallClockGuard {
		t.Errorf("%s took %v, past the %v sanity guard", what, elapsed, wallClockGuard)
	}
}
