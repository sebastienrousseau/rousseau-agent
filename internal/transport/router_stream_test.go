package transport

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

// streamingRunner satisfies both TurnRunner and StreamingTurnRunner
// so Router.runTurn can pick between the two. Tests set streamCalled
// / turnCalled to observe which path fired.
type streamingRunner struct {
	mu          sync.Mutex
	reply       agent.Message
	err         error
	turnCalled  bool
	streamCalls int
	// streamEvents, if non-empty, are sent into the events channel
	// before TurnStream returns, letting a test assert the drain
	// goroutine ran without deadlocking.
	streamEvents []agent.StreamEvent
}

func (s *streamingRunner) Turn(_ context.Context, sess *agent.Session) (agent.Message, error) {
	s.mu.Lock()
	s.turnCalled = true
	s.mu.Unlock()
	if s.err != nil {
		return agent.Message{}, s.err
	}
	sess.Append(s.reply)
	return s.reply, nil
}

func (s *streamingRunner) TurnStream(_ context.Context, sess *agent.Session, events chan<- agent.StreamEvent) (agent.Message, error) {
	// Sender owns the close. Mirrors internal/agent/stream_turn.go's
	// `defer close(events)` in the real TurnStream — the Router relies
	// on this contract and does not close the channel itself.
	defer close(events)
	s.mu.Lock()
	s.streamCalls++
	s.mu.Unlock()
	for _, ev := range s.streamEvents {
		events <- ev
	}
	if s.err != nil {
		return agent.Message{}, s.err
	}
	sess.Append(s.reply)
	return s.reply, nil
}

// discardPublisher is the minimal progress.Publisher used purely to
// mark the context as "supervised" from Router.runTurn's point of
// view. It records nothing.
type discardPublisher struct{}

func (discardPublisher) Publish(_ progress.Event) {}

func TestRouter_UsesTurnStreamWhenRunnerStreamsAndCtxHasPublisher(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	runner := &streamingRunner{
		reply:        agent.NewAssistantText("streamed"),
		streamEvents: []agent.StreamEvent{{Kind: agent.StreamStart}, {Kind: agent.StreamToolUse}},
	}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{})

	ctx := progress.WithPublisher(context.Background(), discardPublisher{})
	reply, err := r.Handle(ctx, IncomingMessage{From: "wa:1", Body: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "streamed", reply)

	assert.Equal(t, 1, runner.streamCalls, "TurnStream must run when ctx has a publisher")
	assert.False(t, runner.turnCalled, "Turn must NOT be called when TurnStream fires")
}

func TestRouter_FallsBackToTurnWhenCtxHasNoPublisher(t *testing.T) {
	// Ctx without publisher = unsupervised (cron, embedded API, tests
	// that don't wire Supervisor). Router must use the plain Turn
	// path — TurnStream would emit into a nil publisher and cost a
	// pointless goroutine.
	store := newMemStore()
	jid := newMemJID()
	runner := &streamingRunner{reply: agent.NewAssistantText("plain")}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{})

	reply, err := r.Handle(context.Background(), IncomingMessage{From: "wa:1", Body: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "plain", reply)

	assert.True(t, runner.turnCalled)
	assert.Equal(t, 0, runner.streamCalls)
}

func TestRouter_FallsBackToTurnWhenRunnerDoesNotStream(t *testing.T) {
	// stubRunner (from router_test.go) satisfies TurnRunner only;
	// even with a publisher on the context, Router must use Turn.
	store := newMemStore()
	jid := newMemJID()
	runner := &stubRunner{reply: agent.NewAssistantText("legacy")}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{})

	ctx := progress.WithPublisher(context.Background(), discardPublisher{})
	reply, err := r.Handle(ctx, IncomingMessage{From: "wa:1", Body: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "legacy", reply)
}

func TestRouter_StreamingRunnerErrorSurfaces(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	runner := &streamingRunner{
		err:          errors.New("provider down"),
		streamEvents: []agent.StreamEvent{{Kind: agent.StreamStart}}, // still emit; drain must not block
	}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{})

	ctx := progress.WithPublisher(context.Background(), discardPublisher{})
	_, err := r.Handle(ctx, IncomingMessage{From: "wa:1", Body: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider down")
	assert.Equal(t, 1, runner.streamCalls)
}

func TestRouter_RunnerClosingEventsDoesNotDoubleClose(t *testing.T) {
	// Regression: the Router used to also `close(events)` after
	// TurnStream returned, which panicked with "close of closed
	// channel" against a runner that (correctly) closed it itself.
	// This test is the streamingRunner (which does close events)
	// exercised through the full Router.Handle → runTurn path,
	// asserting no panic surfaces.
	store := newMemStore()
	jid := newMemJID()
	runner := &streamingRunner{
		reply:        agent.NewAssistantText("closed cleanly"),
		streamEvents: []agent.StreamEvent{{Kind: agent.StreamStart}},
	}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{})

	ctx := progress.WithPublisher(context.Background(), discardPublisher{})
	assert.NotPanics(t, func() {
		reply, err := r.Handle(ctx, IncomingMessage{From: "wa:1", Body: "hi"})
		require.NoError(t, err)
		assert.Equal(t, "closed cleanly", reply)
	})
}

func TestRouter_StreamEventsDrainedEvenWhenManyFired(t *testing.T) {
	// The drain goroutine's job is to keep the events channel from
	// blocking TurnStream. Send more events than the channel buffer
	// (16) so any missing drain would visibly deadlock the test.
	store := newMemStore()
	jid := newMemJID()
	many := make([]agent.StreamEvent, 64)
	for i := range many {
		many[i] = agent.StreamEvent{Kind: agent.StreamTextDelta, Delta: "x"}
	}
	runner := &streamingRunner{
		reply:        agent.NewAssistantText("done"),
		streamEvents: many,
	}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{})

	ctx := progress.WithPublisher(context.Background(), discardPublisher{})
	reply, err := r.Handle(ctx, IncomingMessage{From: "wa:1", Body: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "done", reply)
}
