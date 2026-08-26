package agent

import "context"

// TurnControl is the seam the agent loop uses to honour mid-flight
// user control: pause, resume, cancel, and steering text injected
// while the turn is already running.
//
// Implementations live in internal/control. The loop reaches one via
// the context (WithControl) rather than via Options, because control
// state is per-turn while an *Agent is shared across every
// conversation the daemon serves.
type TurnControl interface {
	// Checkpoint is called at every safe point in the loop: between
	// iterations and between tool calls. It blocks for as long as the
	// user has the turn paused and returns a non-nil error when the
	// turn was cancelled (or ctx is done).
	//
	// Pause therefore takes effect at the next checkpoint, not
	// mid-token: there is no way to suspend a response that is already
	// streaming. Cancellation does not have that limitation — it also
	// cancels the turn's context, which aborts the provider call in
	// flight.
	Checkpoint(ctx context.Context) error
	// Drain returns and clears the text the user steered into the turn
	// since the last call. The loop appends it as user messages before
	// the next model round-trip, which is only meaningful at an
	// iteration boundary — so it is called there and not between tool
	// calls, where the message list must stay a tool_use/tool_result
	// pair.
	Drain() []string
}

// controlKey is the unexported context key for a TurnControl.
type controlKey struct{}

// WithControl returns a context carrying tc, so the agent loop can
// honour control verbs aimed at this turn.
func WithControl(ctx context.Context, tc TurnControl) context.Context {
	return context.WithValue(ctx, controlKey{}, tc)
}

// ControlFrom returns the TurnControl stored by WithControl, or nil
// when the turn is not supervised.
func ControlFrom(ctx context.Context) TurnControl {
	if ctx == nil {
		return nil
	}
	tc, _ := ctx.Value(controlKey{}).(TurnControl)
	return tc
}

// gate runs the context's TurnControl checkpoint, if any. Unsupervised
// turns (no TurnControl on the context) return nil immediately.
func gate(ctx context.Context) error {
	tc := ControlFrom(ctx)
	if tc == nil {
		return nil
	}
	return tc.Checkpoint(ctx)
}

// drainSteered returns the user messages steered into this turn since
// the last iteration boundary, ready to append to the Session.
func drainSteered(ctx context.Context) []Message {
	tc := ControlFrom(ctx)
	if tc == nil {
		return nil
	}
	texts := tc.Drain()
	if len(texts) == 0 {
		return nil
	}
	msgs := make([]Message, 0, len(texts))
	for _, t := range texts {
		msgs = append(msgs, NewUserText(t))
	}
	return msgs
}
