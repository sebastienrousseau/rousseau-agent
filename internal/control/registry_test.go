package control

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

// recorder is a progress.Publisher that keeps everything it is given.
type recorder struct {
	mu     sync.Mutex
	events []progress.Event
}

func (r *recorder) Publish(ev progress.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) kinds() []progress.Kind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]progress.Kind, 0, len(r.events))
	for _, ev := range r.events {
		out = append(out, ev.Kind)
	}
	return out
}

// clock is a manually-advanced time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestRegistry(bus progress.Publisher) (*Registry, *clock) {
	c := newClock()
	return NewRegistry(RegistryOptions{Bus: bus, Now: c.now}), c
}

func TestRegistry_BeginPopulatesTheContext(t *testing.T) {
	rec := &recorder{}
	reg, _ := newTestRegistry(rec)
	ctx, turn := reg.Begin(context.Background(), "wa:1")
	defer turn.End()

	assert.Equal(t, "wa:1", turn.Key())
	assert.Equal(t, TurnRunning, turn.State())
	assert.Equal(t, "wa:1", progress.KeyFrom(ctx))
	assert.Same(t, turn, progress.PublisherFrom(ctx))
	assert.Same(t, turn, agent.ControlFrom(ctx))
	assert.True(t, reg.Running("wa:1"))
	assert.Equal(t, 1, reg.Len())
}

func TestRegistry_LookupAndRunningForUnknownKeys(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	_, ok := reg.Lookup("nobody")
	assert.False(t, ok)
	assert.False(t, reg.Running("nobody"))
	assert.Equal(t, 0, reg.Len())
}

func TestRegistry_NewRegistryDefaultsTheClock(t *testing.T) {
	reg := NewRegistry(RegistryOptions{})
	_, turn := reg.Begin(context.Background(), "k")
	defer turn.End()
	assert.False(t, turn.started.IsZero())
}

func TestTurn_EndDetachesAndIsIdempotent(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	ctx, turn := reg.Begin(context.Background(), "wa:1")
	turn.End()
	turn.End()
	assert.Equal(t, TurnDone, turn.State())
	assert.False(t, reg.Running("wa:1"))
	assert.Error(t, ctx.Err(), "End must release the turn context")
}

func TestTurn_EndDoesNotEvictANewerTurnForTheSameKey(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	_, first := reg.Begin(context.Background(), "wa:1")
	_, second := reg.Begin(context.Background(), "wa:1")
	first.End()

	got, ok := reg.Lookup("wa:1")
	require.True(t, ok)
	assert.Same(t, second, got)
}

func TestTurn_CheckpointPassesThroughWhenRunning(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	ctx, turn := reg.Begin(context.Background(), "k")
	defer turn.End()

	require.NoError(t, turn.Checkpoint(ctx))
	require.NoError(t, turn.Checkpoint(ctx))
	assert.Equal(t, 2, turn.Checkpoints())
}

func TestTurn_CheckpointReportsAnAlreadyCancelledContext(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	parent, cancel := context.WithCancel(context.Background())
	ctx, turn := reg.Begin(parent, "k")
	defer turn.End()
	cancel()
	assert.ErrorIs(t, turn.Checkpoint(ctx), context.Canceled)
}

func TestTurn_PauseBlocksTheCheckpointUntilResume(t *testing.T) {
	rec := &recorder{}
	reg, _ := newTestRegistry(rec)
	ctx, turn := reg.Begin(context.Background(), "k")
	defer turn.End()

	require.True(t, turn.Pause())
	assert.False(t, turn.Pause(), "pausing twice is a no-op")
	assert.Equal(t, TurnPaused, turn.State())

	released := make(chan error, 1)
	go func() { released <- turn.Checkpoint(ctx) }()

	select {
	case <-released:
		t.Fatal("checkpoint returned while paused")
	case <-time.After(20 * time.Millisecond):
	}

	require.True(t, turn.Resume())
	assert.False(t, turn.Resume(), "resuming twice is a no-op")
	require.NoError(t, <-released)
	assert.Contains(t, rec.kinds(), progress.KindPaused)
	assert.Contains(t, rec.kinds(), progress.KindResumed)
}

func TestTurn_PausedCheckpointHonoursContextCancellation(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	parent, cancel := context.WithCancel(context.Background())
	ctx, turn := reg.Begin(parent, "k")
	defer turn.End()

	require.True(t, turn.Pause())
	released := make(chan error, 1)
	go func() { released <- turn.Checkpoint(ctx) }()
	cancel()
	assert.ErrorIs(t, <-released, context.Canceled)
}

func TestTurn_CancelStopsTheContextAndTheCheckpoint(t *testing.T) {
	rec := &recorder{}
	reg, _ := newTestRegistry(rec)
	ctx, turn := reg.Begin(context.Background(), "k")
	defer turn.End()

	require.True(t, turn.Cancel("cancelled by user"))
	assert.False(t, turn.Cancel("again"), "cancelling twice is a no-op")
	assert.Equal(t, TurnCancelled, turn.State())

	err := turn.Checkpoint(ctx)
	assert.ErrorIs(t, err, ErrCancelled)
	assert.ErrorIs(t, err, context.Canceled,
		"ErrCancelled wraps context.Canceled so plain sentinel checks work")
	assert.Error(t, ctx.Err(), "cancel must abort the provider call in flight")
	assert.Contains(t, rec.kinds(), progress.KindCancelled)
}

func TestTurn_CancelReleasesAPausedCheckpoint(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	ctx, turn := reg.Begin(context.Background(), "k")
	defer turn.End()

	require.True(t, turn.Pause())
	released := make(chan error, 1)
	go func() { released <- turn.Checkpoint(ctx) }()
	require.True(t, turn.Cancel(""))
	assert.ErrorIs(t, <-released, ErrCancelled)
}

func TestTurn_EndReleasesAPausedCheckpoint(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	ctx, turn := reg.Begin(context.Background(), "k")
	require.True(t, turn.Pause())
	released := make(chan error, 1)
	go func() { released <- turn.Checkpoint(ctx) }()
	turn.End()
	assert.Error(t, <-released)
}

func TestTurn_EndAfterCancelKeepsTheCancelledState(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	_, turn := reg.Begin(context.Background(), "k")
	require.True(t, turn.Cancel(""))
	turn.End()
	assert.Equal(t, TurnCancelled, turn.State())
	assert.False(t, turn.Cancel(""))
}

func TestTurn_SteerBuffersUntilTheNextCheckpoint(t *testing.T) {
	rec := &recorder{}
	reg, _ := newTestRegistry(rec)
	ctx, turn := reg.Begin(context.Background(), "k")
	defer turn.End()

	assert.False(t, turn.Steer("   "), "blank steering is dropped")
	require.True(t, turn.Steer("as bullets please"))
	require.True(t, turn.Steer("and keep it short"))

	require.NoError(t, turn.Checkpoint(ctx))
	assert.Equal(t, []string{"as bullets please", "and keep it short"}, turn.Drain())
	assert.Empty(t, turn.Drain(), "draining twice yields nothing")
	assert.Contains(t, rec.kinds(), progress.KindSteered)
}

func TestTurn_SteerIsRejectedAfterTheTurnEnds(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	_, cancelled := reg.Begin(context.Background(), "a")
	require.True(t, cancelled.Cancel(""))
	assert.False(t, cancelled.Steer("too late"))

	_, done := reg.Begin(context.Background(), "b")
	done.End()
	assert.False(t, done.Steer("too late"))
}

func TestTurn_PublishForwardsAndFillsTheKey(t *testing.T) {
	rec := &recorder{}
	reg, _ := newTestRegistry(rec)
	_, turn := reg.Begin(context.Background(), "wa:1")
	defer turn.End()

	turn.Publish(progress.Event{Kind: progress.KindToolStarted, Tool: "bash"})
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.events, 1)
	assert.Equal(t, "wa:1", rec.events[0].Key)
}

func TestTurn_PublishWithoutABusIsSafe(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	_, turn := reg.Begin(context.Background(), "k")
	defer turn.End()
	assert.NotPanics(t, func() {
		turn.Publish(progress.Event{Kind: progress.KindToolStarted, Tool: "bash"})
	})
}

func TestTurn_StatusRendersTheLiveView(t *testing.T) {
	reg, clk := newTestRegistry(nil)
	_, turn := reg.Begin(context.Background(), "k")
	defer turn.End()

	turn.Publish(progress.Event{Kind: progress.KindToolStarted, Tool: "bash"})
	turn.Publish(progress.Event{Kind: progress.KindToolFinished, Tool: "bash"})
	turn.Publish(progress.Event{Kind: progress.KindToolStarted, Tool: "read"})
	clk.advance(72 * time.Second)

	status := turn.StatusText()
	assert.Contains(t, status, "1m12s")
	assert.Contains(t, status, "running `read`")
	// The live view now surfaces each completed tool as its own
	// bullet rather than a "N tools" rollup — the rollup only
	// appears on the terminal line where the bullet log may have
	// been trimmed.
	assert.Contains(t, status, progress.GlyphBullet+" bash")

	explain := turn.ExplainText()
	assert.Contains(t, explain, status)
	assert.Contains(t, explain, "recently:")
	assert.Contains(t, explain, "→ bash")
	assert.Contains(t, explain, "✓ bash")
	assert.Contains(t, explain, "/pause, /cancel")
	assert.Equal(t, 72*time.Second, turn.Elapsed())
}

func TestTurn_ExplainWithoutActivityOmitsTheTrail(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	_, turn := reg.Begin(context.Background(), "k")
	defer turn.End()
	assert.NotContains(t, turn.ExplainText(), "recently:")
}

func TestTurn_ActivityTrailIsBounded(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	_, turn := reg.Begin(context.Background(), "k")
	defer turn.End()
	for i := 0; i < maxRecent*3; i++ {
		turn.Publish(progress.Event{Kind: progress.KindToolStarted, Tool: "bash"})
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	assert.Len(t, turn.recent, maxRecent)
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		name string
		ev   progress.Event
		want string
	}{
		{"tool started", progress.Event{Kind: progress.KindToolStarted, Tool: "bash"}, "→ bash"},
		{"tool ok", progress.Event{Kind: progress.KindToolFinished, Tool: "bash"}, "✓ bash"},
		{"tool failed", progress.Event{Kind: progress.KindToolFinished, Tool: "bash", Err: "exit 1"}, "✗ bash: exit 1"},
		{"tool blocked", progress.Event{Kind: progress.KindToolDenied, Tool: "bash"}, "⃠ bash (blocked)"},
		{"subagent started", progress.Event{Kind: progress.KindSubagentStarted}, "→ sub-agent"},
		{"subagent done", progress.Event{Kind: progress.KindSubagentFinished}, "✓ sub-agent"},
		{"plan step", progress.Event{Kind: progress.KindPlanStep, Step: 2, Of: 5, Text: "migrate"}, "• step 2/5 migrate"},
		{"steer", progress.Event{Kind: progress.KindSteered, Text: "shorter"}, "✎ you: shorter"},
		{"noise is dropped", progress.Event{Kind: progress.KindLLMDelta, Text: "abc"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, describe(tc.ev))
		})
	}
}

func TestRegistry_Apply(t *testing.T) {
	t.Run("idle conversations get a single honest answer", func(t *testing.T) {
		reg, _ := newTestRegistry(nil)
		for _, v := range []Verb{VerbStatus, VerbPause, VerbResume, VerbCancel} {
			assert.Equal(t, idleReply, reg.Apply(Decision{Class: ClassControl, Verb: v}, "nobody"))
		}
	})

	t.Run("status", func(t *testing.T) {
		reg, clk := newTestRegistry(nil)
		_, turn := reg.Begin(context.Background(), "k")
		defer turn.End()
		turn.Publish(progress.Event{Kind: progress.KindToolStarted, Tool: "bash"})
		clk.advance(30 * time.Second)

		assert.Contains(t, reg.Apply(Decision{Verb: VerbStatus}, "k"), "running `bash`")
	})

	t.Run("pause, resume and their inverses", func(t *testing.T) {
		reg, clk := newTestRegistry(nil)
		_, turn := reg.Begin(context.Background(), "k")
		defer turn.End()
		clk.advance(62 * time.Second)

		paused := reg.Apply(Decision{Verb: VerbPause}, "k")
		assert.Contains(t, paused, "Paused after 1m02s")
		assert.Contains(t, paused, "/resume", "every state change must show its undo")

		assert.Contains(t, reg.Apply(Decision{Verb: VerbPause}, "k"), "Already paused")
		assert.Contains(t, reg.Apply(Decision{Verb: VerbResume}, "k"), "Resumed")
		assert.Contains(t, reg.Apply(Decision{Verb: VerbResume}, "k"), "Not paused")
	})

	t.Run("cancel", func(t *testing.T) {
		reg, _ := newTestRegistry(nil)
		_, turn := reg.Begin(context.Background(), "k")
		defer turn.End()

		reply := reg.Apply(Decision{Verb: VerbCancel}, "k")
		assert.Contains(t, reply, "Cancelling")
		assert.Equal(t, idleReply, reg.Apply(Decision{Verb: VerbCancel}, "k"))
	})

	t.Run("an unrecognised verb never acts on the turn", func(t *testing.T) {
		reg, _ := newTestRegistry(nil)
		_, turn := reg.Begin(context.Background(), "k")
		defer turn.End()
		assert.Equal(t, idleReply, reg.Apply(Decision{Verb: Verb("nonsense")}, "k"))
		assert.Equal(t, TurnRunning, turn.State())
	})
}

func TestRegistry_TryBeginClaimsAnEmptyKey(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	ctx, turn, ok := reg.TryBegin(context.Background(), "k")
	require.True(t, ok)
	defer turn.End()
	assert.Equal(t, "k", turn.Key())
	assert.Equal(t, TurnRunning, turn.State())
	assert.Same(t, turn, progress.PublisherFrom(ctx))
	assert.Same(t, turn, agent.ControlFrom(ctx))
	assert.True(t, reg.Running("k"))
}

func TestRegistry_TryBeginRejectsAnOccupiedKey(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	_, first := reg.Begin(context.Background(), "k")
	defer first.End()

	ctx, second, ok := reg.TryBegin(context.Background(), "k")
	assert.False(t, ok)
	assert.Nil(t, second)
	assert.Nil(t, ctx)
	assert.Equal(t, 1, reg.Len(), "the occupied slot must remain the first turn")
}

func TestRegistry_TryBeginContextCancelledOnRejection(t *testing.T) {
	// The rejected TryBegin must not leak a live cancel — the goroutine
	// that would have run the turn must have nothing to unwind.
	reg, _ := newTestRegistry(nil)
	_, first := reg.Begin(context.Background(), "k")
	defer first.End()

	ctx, _, ok := reg.TryBegin(context.Background(), "k")
	require.False(t, ok)
	// A nil returned context is the contract; no goroutine to observe.
	assert.Nil(t, ctx)
}

func TestRegistry_TryBeginSerialisesConcurrentClaims(t *testing.T) {
	// N goroutines racing on the same key must produce exactly ONE
	// success — that is the property the Supervisor relies on to stop
	// the "two claude --resume on the same session" bug.
	const n = 64
	reg, _ := newTestRegistry(nil)

	var wg sync.WaitGroup
	var winners int64
	claim := func() {
		defer wg.Done()
		_, turn, ok := reg.TryBegin(context.Background(), "shared")
		if ok {
			atomic.AddInt64(&winners, 1)
			// Hold the slot until every racer has attempted, so no
			// racer can succeed on a re-entry after End.
			<-time.After(20 * time.Millisecond)
			turn.End()
		}
	}
	wg.Add(n)
	for i := 0; i < n; i++ {
		go claim()
	}
	wg.Wait()
	assert.EqualValues(t, 1, winners, "exactly one goroutine may claim the key")
}

func TestRegistry_TryBeginReclaimsAfterEnd(t *testing.T) {
	reg, _ := newTestRegistry(nil)
	_, first, ok := reg.TryBegin(context.Background(), "k")
	require.True(t, ok)
	first.End()

	_, second, ok := reg.TryBegin(context.Background(), "k")
	require.True(t, ok, "the key must be reclaimable after End")
	defer second.End()
	assert.NotSame(t, first, second)
}
