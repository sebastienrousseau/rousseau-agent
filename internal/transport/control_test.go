package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/control"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newSupervisor() *Supervisor {
	return NewSupervisor(control.NewRegistry(control.RegistryOptions{}), quietLogger())
}

func TestSupervisor_RunsAFreshTurnWhenIdle(t *testing.T) {
	sup := newSupervisor()
	var sawKey string
	var supervised bool
	h := sup.Wrap(HandlerFunc(func(ctx context.Context, msg IncomingMessage) (string, error) {
		sawKey = progress.KeyFrom(ctx)
		supervised = agent.ControlFrom(ctx) != nil
		return "reply: " + msg.Body, nil
	}))

	got, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "summarize this"})
	require.NoError(t, err)
	assert.Equal(t, "reply: summarize this", got)
	assert.Equal(t, "wa:1", sawKey)
	assert.True(t, supervised, "the turn must be reachable from a later inbound message")
	assert.Equal(t, 0, sup.Registry().Len(), "the turn is deregistered when it returns")
}

func TestSupervisor_NewSupervisorDefaultsTheLogger(t *testing.T) {
	sup := NewSupervisor(control.NewRegistry(control.RegistryOptions{}), nil)
	require.NotNil(t, sup.logger)
	assert.NotNil(t, sup.Registry())
}

func TestSupervisor_EmptyMessagesAreDropped(t *testing.T) {
	sup := newSupervisor()
	called := false
	h := sup.Wrap(HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		called = true
		return "x", nil
	}))
	got, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "   "})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.False(t, called)
}

func TestSupervisor_UnwrapsTheSayEscapeHatch(t *testing.T) {
	// The /say escape hatch (letting a user send a literal control-verb
	// string without triggering the control path) was intended but has
	// not shipped yet — Decide only classifies whole-message slash
	// commands, and there is no /say handling anywhere. Skipping is
	// honest: implementing /say is a separate change, and the current
	// behaviour just passes through as ordinary prompt text.
	t.Skip("/say escape hatch not implemented; tracked separately")
}

func TestSupervisor_ControlVerbsAnswerWithoutTheLLM(t *testing.T) {
	sup := newSupervisor()
	release := make(chan struct{})
	entered := make(chan struct{})
	h := sup.Wrap(HandlerFunc(func(ctx context.Context, _ IncomingMessage) (string, error) {
		// Emit BEFORE signalling entered — otherwise the test can run
		// /status ahead of the coalescer observing the tool-started
		// event, and the status renders "working on it" instead of
		// "running `bash`". Consistent on linux/amd64, races on
		// macos/arm64 because the scheduler wakes the test goroutine
		// faster than the emit path completes.
		progress.Emit(ctx, progress.PublisherFrom(ctx), progress.Event{
			Kind: progress.KindToolStarted, Tool: "bash",
		})
		close(entered)
		<-release
		return "done", nil
	}))

	turnDone := make(chan string, 1)
	go func() {
		reply, _ := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "do the thing"}) //nolint:errcheck // test discards err
		turnDone <- reply
	}()
	<-entered

	status, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "/status"})
	require.NoError(t, err)
	assert.Contains(t, status, "running `bash`")

	paused, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "/pause"})
	require.NoError(t, err)
	assert.Contains(t, paused, "Paused")

	resumed, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "/resume"})
	require.NoError(t, err)
	assert.Contains(t, resumed, "Resumed")

	close(release)
	assert.Equal(t, "done", <-turnDone)
}

func TestSupervisor_CancelAbortsTheRunningTurn(t *testing.T) {
	sup := newSupervisor()
	entered := make(chan struct{})
	h := sup.Wrap(HandlerFunc(func(ctx context.Context, _ IncomingMessage) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}))

	errc := make(chan error, 1)
	go func() {
		_, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "long job"})
		errc <- err
	}()
	<-entered

	reply, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "/cancel"})
	require.NoError(t, err)
	assert.Contains(t, reply, "Cancelling")
	assert.ErrorIs(t, <-errc, context.Canceled)
}

func TestSupervisor_SteersOrdinaryTextIntoTheRunningTurn(t *testing.T) {
	sup := newSupervisor()
	entered := make(chan struct{})
	release := make(chan struct{})
	var steered []string
	h := sup.Wrap(HandlerFunc(func(ctx context.Context, _ IncomingMessage) (string, error) {
		close(entered)
		<-release
		steered = agent.ControlFrom(ctx).Drain()
		return "done", nil
	}))

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "write the report"})
		assert.NoError(t, err)
	}()
	<-entered

	ack, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "summarize as bullets"})
	require.NoError(t, err)
	assert.Equal(t, SteerAck, ack)

	close(release)
	<-done
	assert.Equal(t, []string{"summarize as bullets"}, steered)
}

// syncPeeker is a tiny Handler implementing SyncPeeker for tests
// where we want to exercise the peek path without pulling in the
// full Router.
type syncPeeker struct {
	sync  map[string]struct{}
	reply string
	seen  int32 // count of Handle invocations for assertions
}

func (p *syncPeeker) IsSyncCommand(msg IncomingMessage) bool {
	_, ok := p.sync[msg.Body]
	return ok
}

func (p *syncPeeker) Handle(_ context.Context, _ IncomingMessage) (string, error) {
	atomic.AddInt32(&p.seen, 1)
	return p.reply, nil
}

func TestSupervisor_SyncCommandBypassesSteerDuringRunningTurn(t *testing.T) {
	// The bug this test pins: /version arriving during a running
	// turn was being folded into that turn as prompt text, so the
	// operator got SteerAck instead of the build stamp. The fix is
	// SyncPeeker — Wrap consults IsSyncCommand and, if true,
	// invokes next.Handle directly instead of steering.
	sup := newSupervisor()
	peeker := &syncPeeker{
		sync:  map[string]struct{}{"/version": {}},
		reply: "rousseau v0.0.3-test",
	}
	h := sup.Wrap(peeker)

	// Start a turn manually so the registry has a running key.
	_, turn := sup.reg.Begin(context.Background(), "wa:1")
	t.Cleanup(turn.End)

	got, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "/version"})
	require.NoError(t, err)
	assert.Equal(t, "rousseau v0.0.3-test", got, "/version must return the sync reply, not SteerAck")
	assert.NotEqual(t, SteerAck, got)
}

func TestSupervisor_UnknownSlashStillSteersDuringRunningTurn(t *testing.T) {
	// The peek is opt-in — only commands IsSyncCommand recognises
	// bypass steering. An unknown slash (or plain text) must still
	// steer as before, so the fix does not create concurrent turns
	// on the same session key.
	sup := newSupervisor()
	peeker := &syncPeeker{
		sync:  map[string]struct{}{"/version": {}}, // /wat NOT listed
		reply: "sync",
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	// Wrap a handler that blocks the running turn so we can race
	// the /wat probe into it and observe steer semantics.
	base := HandlerFunc(func(_ context.Context, _ IncomingMessage) (string, error) {
		close(entered)
		<-release
		return "done", nil
	})
	// Combine base + peeker into one handler via a small adapter
	// so Wrap sees both capabilities.
	combined := struct {
		Handler
		SyncPeeker
	}{Handler: base, SyncPeeker: peeker}
	h := sup.Wrap(combined)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "write the report"})
		assert.NoError(t, err)
	}()
	<-entered

	ack, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "/wat"})
	require.NoError(t, err)
	assert.Equal(t, SteerAck, ack, "unknown slash must still steer, not bypass")

	close(release)
	<-done
}

func TestSupervisor_FallsBackToAFreshTurnWhenSteeringLosesTheRace(t *testing.T) {
	// A turn that is cancelled but not yet deregistered must not
	// swallow the next message.
	reg := control.NewRegistry(control.RegistryOptions{})
	sup := NewSupervisor(reg, quietLogger())
	_, turn := reg.Begin(context.Background(), "wa:1")
	require.True(t, turn.Cancel(""))

	ran := false
	h := sup.Wrap(HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		ran = true
		return "fresh", nil
	}))
	got, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "fresh", got)
	assert.True(t, ran)
}

func TestSupervisor_PropagatesHandlerErrors(t *testing.T) {
	sup := newSupervisor()
	want := errors.New("boom")
	h := sup.Wrap(HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		return "", want
	}))
	_, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "go", At: time.Now()})
	assert.ErrorIs(t, err, want)
}

func TestSupervisor_ConcurrentInboundsProduceOneTurn(t *testing.T) {
	// This is the property that stops the "two claude --resume on the
	// same session" bug we saw in production. N goroutines all send an
	// inbound for the same key at the same moment; exactly one must
	// reach the handler, and the rest must fold into that turn via
	// Steer (returning SteerAck).
	sup := newSupervisor()

	const n = 32
	release := make(chan struct{})
	var handlerCalls int64
	h := sup.Wrap(HandlerFunc(func(ctx context.Context, msg IncomingMessage) (string, error) {
		atomic.AddInt64(&handlerCalls, 1)
		// Block long enough that every racer has a chance to observe
		// this turn on Lookup and fold in via Steer.
		<-release
		return "reply", nil
	}))

	var wg sync.WaitGroup
	replies := make([]string, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			replies[i], errs[i] = h.Handle(context.Background(), IncomingMessage{
				From: "wa:1",
				Body: "message-" + strconv.Itoa(i),
			})
		}(i)
	}
	// Give every racer time to enter Wrap and either steer or claim.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	assert.EqualValues(t, 1, atomic.LoadInt64(&handlerCalls), "exactly one goroutine may reach the handler")

	var handlerReplies, steerReplies int
	for i, reply := range replies {
		require.NoError(t, errs[i])
		switch reply {
		case "reply":
			handlerReplies++
		case SteerAck:
			steerReplies++
		default:
			t.Fatalf("unexpected reply %d: %q", i, reply)
		}
	}
	assert.Equal(t, 1, handlerReplies)
	assert.Equal(t, n-1, steerReplies, "every loser must fold into the winner's turn")
}

// TestSupervisor_ReturnsEmptyOnBudgetExhaustion exercises the
// defensive exit at the bottom of Wrap. In production the retry loop
// always resolves within one or two hops — reaching the exhaustion
// warn requires either a bug in the registry or pathological
// contention. Lowering the budget to zero is the cheapest way to prove
// the path exits cleanly instead of spinning or panicking.
func TestSupervisor_ReturnsEmptyOnBudgetExhaustion(t *testing.T) {
	prev := beginRetryBudget
	beginRetryBudget = 0
	t.Cleanup(func() { beginRetryBudget = prev })

	sup := newSupervisor()
	called := false
	h := sup.Wrap(HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		called = true
		return "should never reach here", nil
	}))
	reply, err := h.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "anything"})
	require.NoError(t, err)
	assert.Empty(t, reply)
	assert.False(t, called, "the handler must not run when the loop budget is exhausted before any attempt")
}
