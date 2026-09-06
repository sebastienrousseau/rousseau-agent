package reliability

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for RunPruner + pruneOnce. Every branch of the
// retention loop is asserted so a future refactor cannot silently
// break the reliability-samples table's growth control.

// fakePruner captures every PruneBefore call so tests can assert
// on the exact cutoff + count arguments the loop passed. Optional
// forcedErr forces the pruner to return an error, exercising the
// WARN branch of RunPruner.
type fakePruner struct {
	mu        sync.Mutex
	cutoffs   []time.Time
	forcedErr error
	returnedN int64
}

func (f *fakePruner) PruneBefore(_ context.Context, cutoff time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cutoffs = append(f.cutoffs, cutoff)
	if f.forcedErr != nil {
		return 0, f.forcedErr
	}
	return f.returnedN, nil
}

func (f *fakePruner) got() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Time, len(f.cutoffs))
	copy(out, f.cutoffs)
	return out
}

func TestRunPruner_NilPrunerReturnsImmediately(t *testing.T) {
	// A nil pruner is a legal way to disable retention (e.g.
	// postgres port not yet shipped, in-memory-only daemon). Must
	// not panic or spin.
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPruner(context.Background(), nil, PruneConfig{})
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunPruner(nil) must return without blocking")
	}
}

func TestRunPruner_RunsOnceImmediatelyOnEntry(t *testing.T) {
	// The loop runs one prune before waiting for a tick — new
	// deployments benefit from disk-space cleanup without waiting
	// a full Interval on first boot.
	fixedNow := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	p := &fakePruner{returnedN: 42}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPruner(ctx, p, PruneConfig{
			Retention: 30 * 24 * time.Hour,
			Now:       func() time.Time { return fixedNow },
			Tick:      make(chan time.Time), // never ticks
		})
	}()

	// Give the immediate prune time to fire, then cancel.
	require.Eventually(t, func() bool {
		return len(p.got()) >= 1
	}, time.Second, 10*time.Millisecond, "immediate prune must fire before any tick")
	cancel()
	<-done

	assert.Len(t, p.got(), 1, "exactly one prune on entry when no tick delivers")
	// Cutoff = now - 30d.
	assert.Equal(t, fixedNow.Add(-30*24*time.Hour), p.got()[0])
}

func TestRunPruner_TicksInvokeAdditionalPrunes(t *testing.T) {
	fixedNow := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	p := &fakePruner{returnedN: 5}
	tick := make(chan time.Time, 3)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPruner(ctx, p, PruneConfig{
			Retention: 7 * 24 * time.Hour,
			Now:       func() time.Time { return fixedNow },
			Tick:      tick,
		})
	}()

	// Wait for the immediate prune.
	require.Eventually(t, func() bool { return len(p.got()) >= 1 }, time.Second, 10*time.Millisecond)
	// Now deliver two ticks; both must invoke prune.
	tick <- fixedNow
	tick <- fixedNow
	require.Eventually(t, func() bool { return len(p.got()) >= 3 }, time.Second, 10*time.Millisecond,
		"two ticks must produce two additional prunes on top of the immediate one")

	cancel()
	<-done
}

func TestRunPruner_ClosedTickTerminatesLoop(t *testing.T) {
	// Closed tick channel must exit RunPruner cleanly (some test
	// harnesses close instead of cancel).
	p := &fakePruner{}
	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPruner(context.Background(), p, PruneConfig{
			Tick: tick,
			Now:  time.Now,
		})
	}()

	close(tick)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closing Tick must terminate RunPruner")
	}
}

func TestRunPruner_PruneFailureLogsWarnAndContinues(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := &fakePruner{forcedErr: errors.New("db locked")}
	tick := make(chan time.Time, 2)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPruner(ctx, p, PruneConfig{
			Tick:   tick,
			Logger: logger,
			Now:    time.Now,
		})
	}()

	// Wait for the immediate failed prune, then deliver a tick and
	// verify the loop kept going.
	require.Eventually(t, func() bool { return len(p.got()) >= 1 }, time.Second, 10*time.Millisecond)
	tick <- time.Now()
	require.Eventually(t, func() bool { return len(p.got()) >= 2 }, time.Second, 10*time.Millisecond)

	cancel()
	<-done

	logs := buf.String()
	assert.Contains(t, logs, "reliability.prune_failed",
		"failure must be logged at WARN with the specific event name")
	assert.Contains(t, logs, "db locked", "underlying error text must surface")
}

func TestRunPruner_ZeroPrunedRowsIsSilent(t *testing.T) {
	// A prune returning 0 rows shouldn't spam the log — real
	// deployments prune on schedule with nothing to remove most
	// of the time.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := &fakePruner{returnedN: 0}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPruner(ctx, p, PruneConfig{
			Tick:   make(chan time.Time),
			Logger: logger,
			Now:    time.Now,
		})
	}()

	require.Eventually(t, func() bool { return len(p.got()) >= 1 }, time.Second, 10*time.Millisecond)
	cancel()
	<-done

	assert.NotContains(t, buf.String(), "reliability.pruned",
		"zero-row prune must not emit the info log — daemon-noise reduction")
}

func TestRunPruner_DefaultsApplied(t *testing.T) {
	p := &fakePruner{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Zero-value PruneConfig — every field defaults.
		RunPruner(ctx, p, PruneConfig{Tick: make(chan time.Time)})
	}()

	require.Eventually(t, func() bool { return len(p.got()) >= 1 }, time.Second, 10*time.Millisecond)
	cancel()
	<-done

	// Cutoff must be ~30 days ago per the default Retention.
	require.Len(t, p.got(), 1)
	elapsed := time.Since(p.got()[0])
	assert.InDelta(t, (30 * 24 * time.Hour).Seconds(), elapsed.Seconds(), 60,
		"default retention must be 30 days (cutoff = now - 30d, within 60s slack)")
}
