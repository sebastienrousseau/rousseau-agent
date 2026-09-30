package transport

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithTurnTimeout_EndsAHungTurn pins the turn deadline: a handler
// that never returns on its own is cancelled at the limit, and the
// sender gets a plain explanation instead of silence.
func TestWithTurnTimeout_EndsAHungTurn(t *testing.T) {
	var sawDeadline bool
	hung := HandlerFunc(func(ctx context.Context, _ IncomingMessage) (string, error) {
		_, sawDeadline = ctx.Deadline()
		<-ctx.Done()
		return "", ctx.Err()
	})
	h := WithTurnTimeout(hung, 20*time.Millisecond, silentLogger())

	done := make(chan struct{})
	var reply string
	var err error
	go func() {
		reply, err = h.Handle(context.Background(), IncomingMessage{From: "x", Body: "go"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn was not cancelled at the deadline")
	}
	require.NoError(t, err)
	assert.True(t, sawDeadline)
	assert.Contains(t, reply, "agent.turn_timeout")
}

func TestWithTurnTimeout_PassesThroughFastTurnsAndErrors(t *testing.T) {
	ok := WithTurnTimeout(HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		return "hi", nil
	}), time.Minute, silentLogger())
	reply, err := ok.Handle(context.Background(), IncomingMessage{})
	require.NoError(t, err)
	assert.Equal(t, "hi", reply)

	// A caller-side cancellation (e.g. /cancel, shutdown) is not a
	// timeout and must not be reported as one.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := WithTurnTimeout(HandlerFunc(func(ctx context.Context, _ IncomingMessage) (string, error) {
		return "", ctx.Err()
	}), time.Minute, silentLogger())
	_, err = cancelled.Handle(ctx, IncomingMessage{})
	assert.ErrorIs(t, err, context.Canceled)

	// Zero disables the deadline entirely.
	var hasDeadline bool
	off := WithTurnTimeout(HandlerFunc(func(ctx context.Context, _ IncomingMessage) (string, error) {
		_, hasDeadline = ctx.Deadline()
		return "", nil
	}), 0, silentLogger())
	_, _ = off.Handle(context.Background(), IncomingMessage{}) //nolint:errcheck // only the deadline matters
	assert.False(t, hasDeadline)
}

// TestWithConcurrencyLimit_CapsParallelTurns pins the global cap on
// concurrent agent turns (each one is a claude process of a few
// hundred MB): excess turns queue instead of all running at once.
func TestWithConcurrencyLimit_CapsParallelTurns(t *testing.T) {
	var mu sync.Mutex
	running, peak := 0, 0
	release := make(chan struct{})
	h := WithConcurrencyLimit(HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		mu.Unlock()
		<-release
		mu.Lock()
		running--
		mu.Unlock()
		return "ok", nil
	}), 2, silentLogger())

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = h.Handle(context.Background(), IncomingMessage{}) //nolint:errcheck // concurrency is the assertion
		}()
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	assert.Equal(t, 2, running, "only the limit runs at once")
	mu.Unlock()
	close(release)
	wg.Wait()
	assert.Equal(t, 2, peak)
}

func TestWithConcurrencyLimit_QueuedTurnHonoursCancel(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	h := WithConcurrencyLimit(HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		<-block
		return "", nil
	}), 1, silentLogger())
	go func() { _, _ = h.Handle(context.Background(), IncomingMessage{}) }() //nolint:errcheck // occupies the slot
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := h.Handle(ctx, IncomingMessage{})
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// Zero means unlimited.
	assert.NotNil(t, WithConcurrencyLimit(HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		return "", nil
	}), 0, silentLogger()))
}

func TestTurnLimiter_SharedAcrossHandlers(t *testing.T) {
	l := NewTurnLimiter(1, silentLogger())
	block := make(chan struct{})
	a := l.Wrap(HandlerFunc(func(context.Context, IncomingMessage) (string, error) { <-block; return "", nil }))
	b := l.Wrap(HandlerFunc(func(context.Context, IncomingMessage) (string, error) { return "b", nil }))
	go func() { _, _ = a.Handle(context.Background(), IncomingMessage{}) }() //nolint:errcheck // holds the only slot
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := b.Handle(ctx, IncomingMessage{})
	assert.ErrorIs(t, err, context.DeadlineExceeded, "second handler shares the first's slot")
	close(block)
	assert.Nil(t, NewTurnLimiter(0, nil))
}
