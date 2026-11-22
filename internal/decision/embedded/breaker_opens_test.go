package embedded

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"elida/internal/decision"
)

// The breaker's transitions are logged, but a log line cannot be charted or
// alerted on as a rate. BreakerOpens counts how many times the breaker went
// from closed (or cooled down) to open; Errors cannot say that, because many
// errors make one trip.
func TestProvider_CountsBreakerOpens(t *testing.T) {
	quietSlog(t)
	now := time.Unix(1_700_000_000, 0)
	p, err := New(context.Background(), Options{
		Enabled: true, Required: true, ModelPath: filepath.Join("testdata", "good"), ThresholdSet: "v1",
		NewPipeline: func(context.Context, string, *Manifest) (Pipeline, error) {
			return scriptedPipeline{}, nil
		},
		BreakerThreshold: 2,
		BreakerCooldown:  time.Minute,
		Clock:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Close() }()
	decide := func(text string) {
		_, _ = p.Decide(context.Background(), decision.Input{Content: text}, []decision.Signal{decision.SignalInjection})
	}
	opens := func(step string, want int64) {
		t.Helper()
		if got := p.Health().BreakerOpens; got != want {
			t.Errorf("%s: BreakerOpens = %d, want %d", step, got, want)
		}
	}

	opens("fresh provider", 0)
	decide("bad")
	opens("one failure, below the threshold", 0)
	decide("bad")
	opens("threshold reached", 1)

	// A failure from a call admitted before the trip lands while the breaker
	// is already open: it is one outage, not a second one.
	p.recordFailure(errors.New("late in-flight failure"), halfOpenProbe{})
	opens("late failure while open", 1)

	// The cooldown elapses and the probe fails: the breaker re-opens.
	now = now.Add(2 * time.Minute)
	decide("bad")
	opens("failed probe re-opens", 2)

	// The next probe succeeds and closes it; a fresh failure run trips it again.
	now = now.Add(2 * time.Minute)
	decide("fine")
	opens("successful probe closes", 2)
	decide("bad")
	decide("bad")
	opens("second trip after recovery", 3)
}
