package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// recPub captures every event emit produces so tests can assert what
// the loop published without a real progress.Bus.
type recPub struct {
	mu     sync.Mutex
	events []progress.Event
}

func (r *recPub) Publish(ev progress.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recPub) snapshot() []progress.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]progress.Event, len(r.events))
	copy(out, r.events)
	return out
}

func TestPublisher_ContextWinsOverOptions(t *testing.T) {
	perTurn := &recPub{}
	shared := &recPub{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(),
		Options{Progress: shared})

	ctx := progress.WithPublisher(context.Background(), perTurn)
	got := a.publisher(ctx)
	assert.Same(t, perTurn, got, "context-scoped publisher must win over Options")
}

func TestPublisher_FallsBackToOptions(t *testing.T) {
	shared := &recPub{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(),
		Options{Progress: shared})

	got := a.publisher(context.Background())
	assert.Same(t, shared, got, "no context publisher → use Options.Progress")
}

func TestPublisher_NilWhenNeitherSet(t *testing.T) {
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(), Options{})
	assert.Nil(t, a.publisher(context.Background()))
}

func TestEmit_DefaultsKeyToSessionID(t *testing.T) {
	rec := &recPub{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(),
		Options{Progress: rec})
	s := NewSession("first turn")
	s.ID = "sess-42"

	a.emit(context.Background(), s, progress.Event{Kind: progress.KindTurnStarted})
	events := rec.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "sess-42", events[0].Key)
}

func TestEmit_ContextKeyWinsOverSession(t *testing.T) {
	rec := &recPub{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(),
		Options{Progress: rec})
	s := NewSession("sess-42")
	ctx := progress.WithKey(context.Background(), "supervisor-key")

	a.emit(ctx, s, progress.Event{Kind: progress.KindTurnStarted})
	events := rec.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "supervisor-key", events[0].Key, "explicit context key wins over session default")
}

func TestEmit_PreservesEventKey(t *testing.T) {
	rec := &recPub{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(),
		Options{Progress: rec})
	s := NewSession("sess-42")

	a.emit(context.Background(), s, progress.Event{Kind: progress.KindTurnStarted, Key: "already-set"})
	events := rec.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "already-set", events[0].Key, "caller-supplied key stays untouched")
}

func TestEmit_NilSessionEmitsWithoutSessionKey(t *testing.T) {
	rec := &recPub{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(),
		Options{Progress: rec})

	a.emit(context.Background(), nil, progress.Event{Kind: progress.KindTurnStarted})
	events := rec.snapshot()
	require.Len(t, events, 1)
	assert.Empty(t, events[0].Key, "no session + no context key → empty key OK")
}

func TestEmitEvent_UsesContextKeyOnly(t *testing.T) {
	rec := &recPub{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(),
		Options{Progress: rec})
	ctx := progress.WithKey(context.Background(), "tool-loop-key")

	a.emitEvent(ctx, progress.Event{Kind: progress.KindToolStarted, Tool: "bash"})
	events := rec.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "tool-loop-key", events[0].Key)
	assert.Equal(t, "bash", events[0].Tool)
}

func TestEmitTerminal_HappyPathKindFinished(t *testing.T) {
	rec := &recPub{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(),
		Options{Progress: rec})
	s := NewSession("s")
	start := time.Now().Add(-100 * time.Millisecond)

	a.emitTerminal(context.Background(), s, start, nil)
	events := rec.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, progress.KindTurnFinished, events[0].Kind)
	assert.Empty(t, events[0].Err)
	assert.Positive(t, int64(events[0].Elapsed))
}

func TestEmitTerminal_CancelledMarksKindCancelled(t *testing.T) {
	rec := &recPub{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(),
		Options{Progress: rec})
	s := NewSession("s")

	a.emitTerminal(context.Background(), s, time.Now(), context.Canceled)
	events := rec.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, progress.KindCancelled, events[0].Kind)
	assert.Contains(t, events[0].Err, "context canceled")
}

func TestEmitTerminal_DeadlineIsCancelled(t *testing.T) {
	// context.DeadlineExceeded classifies as "cancelled" from the
	// user's perspective — they didn't ask for the turn to end this
	// way, but it wasn't a bug either.
	rec := &recPub{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(),
		Options{Progress: rec})
	s := NewSession("s")

	a.emitTerminal(context.Background(), s, time.Now(), context.DeadlineExceeded)
	events := rec.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, progress.KindCancelled, events[0].Kind)
}

func TestEmitTerminal_OtherErrorMarksKindError(t *testing.T) {
	rec := &recPub{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(),
		Options{Progress: rec})
	s := NewSession("s")
	boom := errors.New("provider blew up")

	a.emitTerminal(context.Background(), s, time.Now(), boom)
	events := rec.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, progress.KindError, events[0].Kind)
	assert.Equal(t, "provider blew up", events[0].Err)
}
