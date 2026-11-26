package embedded

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func probeTestProvider(t *testing.T, now *time.Time) *Provider {
	t.Helper()
	quietSlog(t)
	p, err := New(context.Background(), Options{
		Enabled: true, Required: true, ModelPath: filepath.Join("testdata", "good"), ThresholdSet: "v1",
		NewPipeline: func(context.Context, string, *Manifest) (Pipeline, error) {
			return scriptedPipeline{}, nil
		},
		BreakerThreshold: 2,
		BreakerCooldown:  time.Minute,
		Clock:            func() time.Time { return *now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// A call admitted before the trip can still be running when the cooldown ends
// and a probe is admitted. If that late call fails first it must not be taken
// for the probe: the breaker has not changed state, so it is not another open.
func TestBreakerOpens_LateFailureDuringProbeIsNotCountedAsTheProbe(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := probeTestProvider(t, &now)
	boom := errors.New("boom")

	p.recordFailure(boom, halfOpenProbe{})
	p.recordFailure(boom, halfOpenProbe{})
	if got := p.Health().BreakerOpens; got != 1 {
		t.Fatalf("after the trip: BreakerOpens = %d, want 1", got)
	}

	now = now.Add(2 * time.Minute)
	ok, probe := p.admit()
	if !ok || !probe.held {
		t.Fatalf("admit after the cooldown = %v, %+v, want the single probe", ok, probe)
	}

	// The late in-flight call fails while the probe is out.
	p.recordFailure(boom, halfOpenProbe{})
	if got := p.Health().BreakerOpens; got != 1 {
		t.Errorf("late failure during the probe: BreakerOpens = %d, want 1", got)
	}

	// The probe itself succeeds: the breaker closes, and no open is added.
	p.recordSuccess()
	if got := p.Health().BreakerOpens; got != 1 {
		t.Errorf("after the probe succeeded: BreakerOpens = %d, want 1", got)
	}
}

func TestBreakerOpens_FailedProbeIsCounted(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := probeTestProvider(t, &now)
	boom := errors.New("boom")

	p.recordFailure(boom, halfOpenProbe{})
	p.recordFailure(boom, halfOpenProbe{})
	now = now.Add(2 * time.Minute)
	_, probe := p.admit()
	p.recordFailure(boom, probe)
	if got := p.Health().BreakerOpens; got != 2 {
		t.Errorf("failed probe: BreakerOpens = %d, want 2", got)
	}
}
