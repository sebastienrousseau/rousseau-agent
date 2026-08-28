package progress

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSink records everything the Reporter delivers. editable toggles
// whether it also satisfies Editor (exercised via fakeEditorSink).
type fakeSink struct {
	mu       sync.Mutex
	sent     []Update
	sendErr  error
	handleID Handle
}

func (f *fakeSink) Send(_ context.Context, u Update) (Handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return "", f.sendErr
	}
	f.sent = append(f.sent, u)
	return f.handleID, nil
}

func (f *fakeSink) sends() []Update {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Update(nil), f.sent...)
}

type fakeEditorSink struct {
	fakeSink
	edits   []Update
	handles []Handle
	editErr error
}

func (f *fakeEditorSink) Edit(_ context.Context, h Handle, u Update) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return f.editErr
	}
	f.handles = append(f.handles, h)
	f.edits = append(f.edits, u)
	return nil
}

var errSinkDown = errors.New("transport down")

// harness wires a bus, a subscription, a manual clock and a manual
// tick channel so the whole Reporter can be driven without sleeping.
type harness struct {
	bus  *Bus
	sub  *Subscription
	tick chan time.Time
	now  time.Time
	mu   sync.Mutex
	rep  *Reporter
	done chan struct{}
}

func newHarness(t *testing.T, sink Sink, pol Policy) *harness {
	t.Helper()
	h := &harness{
		bus:  NewBus(BusOptions{RingSize: 4}),
		tick: make(chan time.Time),
		now:  base,
		done: make(chan struct{}),
	}
	h.sub = h.bus.Subscribe("k")
	h.rep = NewReporter(ReporterConfig{
		Sub:    h.sub,
		Sink:   sink,
		Policy: pol,
		Start:  base,
		Now:    h.clock,
		Tick:   h.tick,
		Logger: silentLogger(),
	})
	return h
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func (h *harness) run(ctx context.Context) {
	go func() {
		defer close(h.done)
		h.rep.Run(ctx)
	}()
}

// pulse advances the clock and delivers one tick, waiting for the
// Reporter to consume it so assertions are race-free.
func (h *harness) pulse(d time.Duration) {
	h.advance(d)
	h.tick <- time.Time{}
	// A second tick send only completes once the first has been fully
	// processed, which gives us a happens-before edge for assertions.
	h.tick <- time.Time{}
}

func TestReporter_RunIsANoOpWithoutCollaborators(t *testing.T) {
	assert.NotPanics(t, func() {
		NewReporter(ReporterConfig{Logger: silentLogger()}).Run(context.Background())
	})
	b := NewBus(BusOptions{})
	sub := b.Subscribe("k")
	assert.NotPanics(t, func() {
		NewReporter(ReporterConfig{Sub: sub, Logger: silentLogger()}).Run(context.Background())
	})
}

func TestNewReporter_AppliesDefaults(t *testing.T) {
	r := NewReporter(ReporterConfig{Sink: &fakeSink{}})
	assert.Equal(t, DefaultResolution, r.cfg.Resolution)
	assert.Equal(t, DefaultMaxFailures, r.cfg.MaxFailures)
	require.NotNil(t, r.cfg.Logger)
	require.NotNil(t, r.cfg.Now)
	assert.False(t, r.cfg.Start.IsZero())
}

func TestReporter_PostsFirstUpdateThenEditsInPlace(t *testing.T) {
	sink := &fakeEditorSink{}
	sink.handleID = "msg-1"
	h := newHarness(t, sink, Policy{})
	ctx := context.Background()
	h.run(ctx)

	h.bus.Publish(Event{Key: "k", Kind: KindToolStarted, Tool: "bash"})
	h.pulse(8 * time.Second)
	require.Len(t, sink.sends(), 1)
	assert.Contains(t, sink.sends()[0].Text, "running `bash`")

	h.bus.Publish(Event{Key: "k", Kind: KindToolFinished, Tool: "bash"})
	h.pulse(10 * time.Second)

	h.bus.Publish(Event{Key: "k", Kind: KindTurnFinished})
	<-h.done

	sink.mu.Lock()
	defer sink.mu.Unlock()
	assert.Len(t, sink.sent, 1, "only the first update is a new message")
	require.GreaterOrEqual(t, len(sink.edits), 2)
	assert.Equal(t, Handle("msg-1"), sink.handles[0])
	last := sink.edits[len(sink.edits)-1]
	assert.True(t, last.Terminal)
	assert.Contains(t, last.Text, GlyphBullet+" done in")
	assert.Equal(t, 3, h.rep.Sent())
}

func TestReporter_PostsNewMessagesWhenTheSinkCannotEdit(t *testing.T) {
	sink := &fakeSink{}
	h := newHarness(t, sink, Policy{})
	h.run(context.Background())

	h.bus.Publish(Event{Key: "k", Kind: KindTurnStarted})
	h.pulse(8 * time.Second)
	// MinInterval (25s), not MinEditInterval, applies.
	h.bus.Publish(Event{Key: "k", Kind: KindToolStarted, Tool: "bash"})
	h.pulse(10 * time.Second)
	assert.Len(t, sink.sends(), 1)
	h.pulse(15 * time.Second)
	assert.Len(t, sink.sends(), 2)

	h.bus.Publish(Event{Key: "k", Kind: KindError, Err: "provider blew up"})
	<-h.done

	got := sink.sends()
	require.Len(t, got, 3)
	assert.False(t, got[2].Replace)
	assert.Contains(t, got[2].Text, GlyphFailed+" failed after")
	assert.Contains(t, got[2].Text, "provider blew up")
}

func TestReporter_StaleEditHandleFallsBackToANewMessage(t *testing.T) {
	sink := &fakeEditorSink{}
	sink.handleID = "msg-1"
	h := newHarness(t, sink, Policy{})
	h.run(context.Background())

	h.bus.Publish(Event{Key: "k", Kind: KindTurnStarted})
	h.pulse(8 * time.Second)
	require.Len(t, sink.sends(), 1)

	sink.mu.Lock()
	sink.editErr = errSinkDown
	sink.mu.Unlock()

	h.bus.Publish(Event{Key: "k", Kind: KindToolStarted, Tool: "bash"})
	h.pulse(10 * time.Second) // edit attempt fails, handle dropped

	sink.mu.Lock()
	sink.editErr = nil
	sink.mu.Unlock()

	h.bus.Publish(Event{Key: "k", Kind: KindToolFinished, Tool: "bash"})
	h.pulse(10 * time.Second) // no handle → posts a new message

	assert.Len(t, sink.sends(), 2)
}

func TestReporter_BreakerStopsProgressButStillTriesTheFinalUpdate(t *testing.T) {
	sink := &fakeSink{sendErr: errSinkDown}
	h := newHarness(t, sink, Policy{MinInterval: time.Second})
	h.run(context.Background())

	for i := 0; i < 4; i++ {
		h.bus.Publish(Event{Key: "k", Kind: KindToolStarted, Tool: "bash"})
		h.pulse(10 * time.Second)
	}
	require.GreaterOrEqual(t, h.rep.failures, DefaultMaxFailures)

	// Breaker is open: further progress updates are not even attempted.
	before := h.rep.failures
	h.bus.Publish(Event{Key: "k", Kind: KindToolStarted, Tool: "read"})
	h.pulse(10 * time.Second)
	assert.Equal(t, before, h.rep.failures, "no further attempts while tripped")

	// The terminal update is always attempted; let it succeed.
	sink.mu.Lock()
	sink.sendErr = nil
	sink.mu.Unlock()
	h.bus.Publish(Event{Key: "k", Kind: KindTurnFinished})
	<-h.done

	got := sink.sends()
	require.Len(t, got, 1)
	assert.True(t, got[0].Terminal)
}

func TestReporter_SurfacesDroppedEvents(t *testing.T) {
	sink := &fakeSink{}
	h := newHarness(t, sink, Policy{})
	h.run(context.Background())

	// Ring size is 4; overflow it before the reporter can drain.
	for i := 0; i < 12; i++ {
		h.bus.Publish(Event{Key: "k", Kind: KindThinking, Iteration: i + 1})
	}
	h.pulse(30 * time.Second)
	require.NotEmpty(t, sink.sends())
	assert.Contains(t, sink.sends()[0].Text, "events dropped")
}

func TestReporter_StopsWhenContextIsCancelled(t *testing.T) {
	sink := &fakeSink{}
	h := newHarness(t, sink, Policy{})
	ctx, cancel := context.WithCancel(context.Background())
	h.run(ctx)
	cancel()
	<-h.done
	assert.Empty(t, sink.sends())
}

func TestReporter_StopsWhenTheSubscriptionCloses(t *testing.T) {
	sink := &fakeSink{}
	h := newHarness(t, sink, Policy{})
	h.run(context.Background())
	h.sub.Close()
	<-h.done
	assert.Empty(t, sink.sends())
}

func TestReporter_OwnsATickerWhenNoneIsInjected(t *testing.T) {
	sink := &fakeSink{}
	bus := NewBus(BusOptions{})
	sub := bus.Subscribe("k")
	rep := NewReporter(ReporterConfig{
		Sub:        sub,
		Sink:       sink,
		Policy:     Policy{FirstDelay: time.Nanosecond},
		Start:      time.Now().Add(-time.Hour),
		Resolution: time.Millisecond,
		Logger:     silentLogger(),
	})
	done := make(chan struct{})
	go func() { defer close(done); rep.Run(context.Background()) }()

	bus.Publish(Event{Key: "k", Kind: KindToolStarted, Tool: "bash"})
	require.Eventually(t, func() bool { return len(sink.sends()) > 0 }, 2*time.Second, time.Millisecond)
	sub.Close()
	<-done
}
