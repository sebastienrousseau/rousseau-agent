package control

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

// ErrCancelled is returned from Turn.Checkpoint after the user
// cancelled the turn. The agent loop unwinds on it.
// It wraps context.Canceled so callers that only know about the
// standard sentinel still classify the turn correctly.
var ErrCancelled = fmt.Errorf("control: turn cancelled by user: %w", context.Canceled)

// TurnState is the lifecycle state of an in-flight turn.
type TurnState string

const (
	// TurnRunning is the normal state.
	TurnRunning TurnState = "running"
	// TurnPaused means the next checkpoint will block.
	TurnPaused TurnState = "paused"
	// TurnCancelled means the next checkpoint will fail and the turn's
	// context has been cancelled.
	TurnCancelled TurnState = "cancelled"
	// TurnDone means the turn finished and released its resources.
	TurnDone TurnState = "done"
)

// maxRecent bounds the activity trail kept for the "explain" verb.
const maxRecent = 6

// Turn is one in-flight agent turn, and the thing control verbs act
// on. It is safe for concurrent use: the agent loop calls Checkpoint
// from the turn goroutine while the transport calls Pause / Cancel /
// Steer from the inbound goroutine.
//
// Turn is also a progress.Publisher. The supervisor splices it in
// front of the shared Bus so that it sees every progress event on its
// way past and can answer "status" and "explain" without the agent
// loop, the bus, or the reporter knowing it is there.
type Turn struct {
	key     string
	started time.Time
	now     func() time.Time
	cancel  context.CancelFunc
	next    progress.Publisher
	reg     *Registry

	mu          sync.Mutex
	state       TurnState
	resume      chan struct{}
	steer       []string
	co          *progress.Coalescer
	recent      []string
	checkpoints int
}

// Key returns the conversation key this turn belongs to.
func (t *Turn) Key() string { return t.key }

// State returns the current lifecycle state.
func (t *Turn) State() TurnState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Elapsed returns how long the turn has been running.
func (t *Turn) Elapsed() time.Duration { return t.now().Sub(t.started) }

// Checkpoint satisfies agent.TurnControl: it blocks while the turn is
// paused and reports cancellation.
func (t *Turn) Checkpoint(ctx context.Context) error {
	for {
		t.mu.Lock()
		switch t.state {
		case TurnCancelled:
			t.mu.Unlock()
			return ErrCancelled
		case TurnPaused:
			wait := t.resume
			t.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			t.checkpoints++
			t.mu.Unlock()
			return ctx.Err()
		}
	}
}

// Drain satisfies agent.TurnControl: it returns and clears the text
// the user steered into this turn.
func (t *Turn) Drain() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	steered := t.steer
	t.steer = nil
	return steered
}

// Checkpoints reports how many checkpoints the loop has passed.
func (t *Turn) Checkpoints() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.checkpoints
}

// Steer buffers text for the running turn to pick up at its next
// checkpoint, where it is appended as an ordinary user message.
//
// This is what makes "almost every verb is prompt content" workable
// rather than merely safe: a mid-flight "as bullets please" lands in
// the turn already running instead of being queued behind it or
// racing a second turn against the same Session.
func (t *Turn) Steer(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	t.mu.Lock()
	if t.state == TurnCancelled || t.state == TurnDone {
		t.mu.Unlock()
		return false
	}
	t.steer = append(t.steer, text)
	t.mu.Unlock()
	t.Publish(progress.Event{Key: t.key, Kind: progress.KindSteered, Text: text})
	return true
}

// Pause suspends the turn at its next checkpoint. Reports false when
// the turn was not running.
func (t *Turn) Pause() bool {
	t.mu.Lock()
	if t.state != TurnRunning {
		t.mu.Unlock()
		return false
	}
	t.state = TurnPaused
	t.resume = make(chan struct{})
	t.mu.Unlock()
	t.Publish(progress.Event{Key: t.key, Kind: progress.KindPaused})
	return true
}

// Resume releases a paused turn. Reports false when it was not paused.
func (t *Turn) Resume() bool {
	t.mu.Lock()
	if t.state != TurnPaused {
		t.mu.Unlock()
		return false
	}
	t.state = TurnRunning
	close(t.resume)
	t.resume = nil
	t.mu.Unlock()
	t.Publish(progress.Event{Key: t.key, Kind: progress.KindResumed})
	return true
}

// Cancel aborts the turn: it cancels the turn's context (which stops
// the provider call in flight) and makes the next checkpoint return
// ErrCancelled. Reports false when the turn had already finished.
func (t *Turn) Cancel(reason string) bool {
	t.mu.Lock()
	if t.state == TurnCancelled || t.state == TurnDone {
		t.mu.Unlock()
		return false
	}
	t.state = TurnCancelled
	if t.resume != nil {
		close(t.resume)
		t.resume = nil
	}
	t.mu.Unlock()
	t.Publish(progress.Event{Key: t.key, Kind: progress.KindCancelled, Text: reason})
	t.cancel()
	return true
}

// End marks the turn finished, releases anything blocked on a pause,
// and detaches it from its Registry. Safe to call multiple times;
// callers should defer it immediately after Begin.
func (t *Turn) End() {
	t.mu.Lock()
	if t.state != TurnCancelled {
		t.state = TurnDone
	}
	if t.resume != nil {
		close(t.resume)
		t.resume = nil
	}
	t.mu.Unlock()
	t.cancel()
	t.reg.remove(t)
}

// Publish satisfies progress.Publisher: it folds the event into the
// turn's own view (so "status" can be answered instantly) and forwards
// it downstream.
func (t *Turn) Publish(ev progress.Event) {
	if ev.Key == "" {
		ev.Key = t.key
	}
	t.mu.Lock()
	t.co.Absorb(ev)
	if line := describe(ev); line != "" {
		t.recent = append(t.recent, line)
		if len(t.recent) > maxRecent {
			t.recent = t.recent[len(t.recent)-maxRecent:]
		}
	}
	t.mu.Unlock()
	if t.next != nil {
		t.next.Publish(ev)
	}
}

// StatusText renders the "status" reply: exactly the same view the
// live progress updates show, so the two can never disagree.
func (t *Turn) StatusText() string {
	t.mu.Lock()
	st := t.co.State()
	t.mu.Unlock()
	return progress.Render(st, t.Elapsed())
}

// ExplainText renders the "explain" reply: the status view plus the
// recent activity trail.
func (t *Turn) ExplainText() string {
	t.mu.Lock()
	st := t.co.State()
	recent := append([]string(nil), t.recent...)
	t.mu.Unlock()

	var b strings.Builder
	b.WriteString(progress.Render(st, t.Elapsed()))
	if len(recent) > 0 {
		b.WriteString("\n\nrecently:")
		for _, line := range recent {
			b.WriteString("\n  " + line)
		}
	}
	b.WriteString("\n\n(/pause, /cancel, or just tell me what to change.)")
	return b.String()
}

// describe renders one activity-trail line, or "" for events too noisy
// to keep (streamed text fragments).
func describe(ev progress.Event) string {
	switch ev.Kind {
	case progress.KindToolStarted:
		return "→ " + ev.Tool
	case progress.KindToolFinished:
		if ev.Err != "" {
			return "✗ " + ev.Tool + ": " + ev.Err
		}
		return "✓ " + ev.Tool
	case progress.KindToolDenied:
		return "⃠ " + ev.Tool + " (blocked)"
	case progress.KindSubagentStarted:
		return "→ sub-agent"
	case progress.KindSubagentFinished:
		return "✓ sub-agent"
	case progress.KindPlanStep:
		return fmt.Sprintf("• step %d/%d %s", ev.Step, ev.Of, ev.Text)
	case progress.KindSteered:
		return "✎ you: " + ev.Text
	default:
		return ""
	}
}

// RegistryOptions configures a Registry.
type RegistryOptions struct {
	// Bus receives every progress event after the Turn has observed
	// it. Nil discards them — control still works, the user just gets
	// no live updates.
	Bus progress.Publisher
	// Policy is handed to each Turn's internal coalescer so "status"
	// renders identically to the live updates. Zero uses the defaults.
	Policy progress.Policy
	// Now is the clock. Nil uses time.Now.
	Now func() time.Time
}

// Registry tracks the in-flight turn for each conversation, which is
// how an inbound message reaches a turn that is already running.
// Safe for concurrent use.
type Registry struct {
	mu    sync.Mutex
	turns map[string]*Turn
	opts  RegistryOptions
}

// NewRegistry constructs a Registry.
func NewRegistry(opts RegistryOptions) *Registry {
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Registry{turns: map[string]*Turn{}, opts: opts}
}

// Begin registers a new in-flight turn for key and returns the context
// the turn must run under. The returned context carries:
//
//   - a cancellation the user's "cancel" verb triggers,
//   - the progress routing key, so the agent loop's events reach this
//     conversation's reporter,
//   - the Turn as the per-turn progress.Publisher,
//   - the Turn as the agent.TurnControl checkpoint gate.
//
// The caller MUST defer Turn.End.
func (r *Registry) Begin(ctx context.Context, key string) (context.Context, *Turn) {
	tctx, cancel := context.WithCancel(ctx)
	now := r.opts.Now()
	t := &Turn{
		key:     key,
		started: now,
		now:     r.opts.Now,
		cancel:  cancel,
		next:    r.opts.Bus,
		reg:     r,
		state:   TurnRunning,
		co:      progress.NewCoalescer(key, r.opts.Policy, now),
	}
	r.mu.Lock()
	r.turns[key] = t
	r.mu.Unlock()

	tctx = progress.WithKey(tctx, key)
	tctx = progress.WithPublisher(tctx, t)
	tctx = agent.WithControl(tctx, t)
	return tctx, t
}

// Lookup returns the in-flight turn for key.
func (r *Registry) Lookup(key string) (*Turn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.turns[key]
	return t, ok
}

// Running reports whether a turn is in flight for key.
func (r *Registry) Running(key string) bool {
	_, ok := r.Lookup(key)
	return ok
}

// Len returns how many turns are in flight.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.turns)
}

// remove detaches t from the registry, but only if it is still the
// current turn for its key — a slow End must not evict a newer turn.
func (r *Registry) remove(t *Turn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.turns[t.key]; ok && cur == t {
		delete(r.turns, t.key)
	}
}

// idleReply is what every control verb gets when nothing is running.
const idleReply = "Nothing running right now."

// Apply executes a control Decision against the turn in flight for
// key and returns the reply to send. It never touches the LLM, so it
// answers instantly and for free.
//
// Every reply states the inverse operation. A classifier that guesses
// must always show its undo.
func (r *Registry) Apply(d Decision, key string) string {
	t, ok := r.Lookup(key)
	if !ok {
		return idleReply
	}
	switch d.Verb {
	case VerbStatus:
		return t.StatusText()
	case VerbPause:
		if !t.Pause() {
			return "Already " + string(t.State()) + "."
		}
		return "⏸️ Paused after " + progress.FormatDuration(t.Elapsed()) +
			". Send /resume to continue, or /cancel to stop."
	case VerbResume:
		if !t.Resume() {
			return "Not paused — still working."
		}
		return "▶️ Resumed."
	case VerbCancel:
		if !t.Cancel("cancelled by user") {
			return idleReply
		}
		return "⏹️ Cancelling after " + progress.FormatDuration(t.Elapsed()) + " — /status to confirm."
	default:
		return idleReply
	}
}

// Compile-time checks.
var (
	_ agent.TurnControl  = (*Turn)(nil)
	_ progress.Publisher = (*Turn)(nil)
)
