// Package progress carries live, coalesced progress updates from a
// running agent turn to whichever transport the user is talking on.
//
// The flow is: emitters (the agent loop, sub-agents, the plan
// executor, the cron scheduler) publish [Event] values on a [Bus]
// keyed by conversation; a transport subscribes for its conversation
// and runs a [Reporter], which folds the stream into a single [State]
// via a [Coalescer] and pushes rendered [Update] values to a [Sink] at
// a rate a chat client can survive.
//
// Design notes, including why the throttle constants are what they
// are, live in docs/progress-updates.md.
//
// Nothing in this package performs I/O, reads the wall clock outside
// of injectable seams, or imports another rousseau package. That is
// deliberate: the whole policy is a pure state machine so it can be
// tested to the second without sleeping.
package progress

import (
	"context"
	"time"
)

// Kind categorises a progress event.
type Kind string

const (
	// KindTurnStarted fires once when a turn is accepted.
	KindTurnStarted Kind = "turn_started"
	// KindThinking fires at the start of each model round-trip.
	KindThinking Kind = "thinking"
	// KindLLMDelta carries a fragment of streamed assistant text. It
	// is lifted from agent.StreamEvent's StreamTextDelta.
	KindLLMDelta Kind = "llm_delta"
	// KindToolStarted fires immediately before a tool executes.
	KindToolStarted Kind = "tool_started"
	// KindToolFinished fires after a tool returns. Event.Err is set
	// when the tool failed.
	KindToolFinished Kind = "tool_finished"
	// KindToolDenied fires when an approver or hook blocks a tool.
	KindToolDenied Kind = "tool_denied"
	// KindSubagentStarted fires when a sub-agent task is dispatched.
	KindSubagentStarted Kind = "subagent_started"
	// KindSubagentFinished fires when a sub-agent task completes.
	KindSubagentFinished Kind = "subagent_finished"
	// KindPlanStep fires at each plan step boundary.
	KindPlanStep Kind = "plan_step"
	// KindCronStarted fires when a scheduled job begins.
	KindCronStarted Kind = "cron_started"
	// KindCronFinished fires when a scheduled job finishes.
	KindCronFinished Kind = "cron_finished"
	// KindPaused fires when the user pauses the turn.
	KindPaused Kind = "paused"
	// KindResumed fires when the user resumes a paused turn.
	KindResumed Kind = "resumed"
	// KindSteered fires when the user injects text into a running turn.
	KindSteered Kind = "steered"
	// KindCancelled is terminal: the user cancelled the turn.
	KindCancelled Kind = "cancelled"
	// KindTurnFinished is terminal: the turn completed normally.
	KindTurnFinished Kind = "turn_finished"
	// KindError is terminal: the turn failed.
	KindError Kind = "error"
)

// Terminal reports whether a Kind ends the turn. Terminal events
// bypass the throttle and flush immediately.
func (k Kind) Terminal() bool {
	switch k {
	case KindTurnFinished, KindError, KindCancelled:
		return true
	default:
		return false
	}
}

// Event is a single progress observation.
type Event struct {
	// Key routes the event to the subscriber for one conversation.
	// Publishers normally take it from the context via KeyFrom.
	Key string
	// Kind identifies the variant.
	Kind Kind
	// Tool names the tool for KindTool* events.
	Tool string
	// Text is the headline for the event: the streamed fragment for
	// KindLLMDelta, the final answer for KindTurnFinished, a free
	// description otherwise.
	Text string
	// Detail is optional secondary text (e.g. a truncated tool input).
	Detail string
	// Iteration is the 1-based agent-loop round-trip this event
	// belongs to. Zero when not applicable.
	Iteration int
	// Step and Of carry a plan cursor ("step 2 of 5"). Both zero when
	// no plan is running.
	Step, Of int
	// Elapsed is the duration the event describes, for events that
	// measure something (a finished tool, a finished turn).
	Elapsed time.Duration
	// Err is a non-empty error string when the event reports failure.
	Err string
	// At is the observation time. Publishers may leave it zero; Bus
	// stamps it on publish.
	At time.Time
}

// Publisher is the narrow emit-side seam. The agent loop holds one of
// these, never a *Bus, so callers can substitute a no-op.
type Publisher interface {
	// Publish delivers ev to every subscriber for ev.Key. It must not
	// block: a chat transport must never be able to stall the agent
	// loop.
	Publish(ev Event)
}

// PublisherFunc adapts a function to Publisher.
type PublisherFunc func(ev Event)

// Publish satisfies Publisher.
func (f PublisherFunc) Publish(ev Event) { f(ev) }

// Nop is a Publisher that discards every event. Use it instead of a
// nil check at call sites.
type Nop struct{}

// Publish satisfies Publisher.
func (Nop) Publish(Event) {}

// ctxKey is the unexported context key type for the conversation key.
type ctxKey struct{}

// pubKey is the unexported context key type for a per-turn Publisher.
type pubKey struct{}

// WithKey returns a context carrying the conversation key progress
// events for this request should be published under. Transports set
// it; the agent loop reads it back with KeyFrom.
func WithKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, ctxKey{}, key)
}

// KeyFrom returns the conversation key stored by WithKey, or "" when
// the context carries none.
func KeyFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	key, _ := ctx.Value(ctxKey{}).(string)
	return key
}

// WithPublisher returns a context carrying the Publisher that events
// for this request should go to. It exists so a supervisor can splice
// a per-turn decorator (one that observes events on their way past)
// in front of the shared Bus without the agent loop learning about
// either. Absent, callers fall back to their configured publisher.
func WithPublisher(ctx context.Context, p Publisher) context.Context {
	return context.WithValue(ctx, pubKey{}, p)
}

// PublisherFrom returns the Publisher stored by WithPublisher, or nil.
func PublisherFrom(ctx context.Context) Publisher {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(pubKey{}).(Publisher)
	return p
}

// Emit is a nil-safe convenience for publishers held in optional
// fields: it fills in Key from ctx when the event has none, stamps At,
// and drops the event when p is nil.
func Emit(ctx context.Context, p Publisher, ev Event) {
	if p == nil {
		return
	}
	if ev.Key == "" {
		ev.Key = KeyFrom(ctx)
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	p.Publish(ev)
}
