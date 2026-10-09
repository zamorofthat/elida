package unit

import (
	"context"
	"errors"
	"testing"
	"time"

	"elida/internal/decision/embedded"
)

// hangingClosePipeline is a working pipeline whose Close blocks until
// released, standing in for a backend waiting on a hung inference.
type hangingClosePipeline struct {
	embedded.Pipeline
	release chan struct{}
}

func (h hangingClosePipeline) Close() error {
	<-h.release
	return nil
}

func TestProviderCloseContext_GivesUpAtTheDeadline(t *testing.T) {
	quietSlog(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	inner := fakeFactory()
	opts := goodOptions(t)
	opts.NewPipeline = func(ctx context.Context, dir string, m *embedded.Manifest) (embedded.Pipeline, error) {
		p, err := inner(ctx, dir, m)
		if err != nil {
			return nil, err
		}
		return hangingClosePipeline{Pipeline: p, release: release}, nil
	}
	p, err := embedded.New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = p.CloseContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext = %v, want the context's deadline error", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("CloseContext waited %v past its deadline", el)
	}
}

func TestProviderCloseContext_ReturnsWhenCloseFinishes(t *testing.T) {
	quietSlog(t)
	p, err := embedded.New(context.Background(), goodOptions(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.CloseContext(context.Background()); err != nil {
		t.Fatalf("CloseContext: %v", err)
	}
}
