package agent

import (
	"context"
	"errors"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

// publisher resolves which progress.Publisher this turn should use.
// A per-turn publisher installed on the context wins over the Agent's
// shared one, because Options is shared across every conversation the
// daemon serves and the context is not.
func (a *Agent) publisher(ctx context.Context) progress.Publisher {
	if p := progress.PublisherFrom(ctx); p != nil {
		return p
	}
	return a.opts.Progress
}

// emit publishes ev for the given Session, defaulting the routing key
// to the session ID when the context carries none (CLI, embedded use,
// tests). Nil publishers are handled by progress.Emit.
func (a *Agent) emit(ctx context.Context, s *Session, ev progress.Event) {
	if ev.Key == "" {
		if ev.Key = progress.KeyFrom(ctx); ev.Key == "" && s != nil {
			ev.Key = s.ID
		}
	}
	progress.Emit(ctx, a.publisher(ctx), ev)
}

// emitEvent is emit for call sites that have no Session in scope
// (the tool loop); the context key is the only source of routing.
func (a *Agent) emitEvent(ctx context.Context, ev progress.Event) {
	a.emit(ctx, nil, ev)
}

// emitTerminal publishes the closing event of a turn, choosing the
// Kind from the error: a cancelled turn reads very differently from a
// failed one, and the user needs to be told which happened.
func (a *Agent) emitTerminal(ctx context.Context, s *Session, start time.Time, err error) {
	ev := progress.Event{Kind: progress.KindTurnFinished, Elapsed: time.Since(start)}
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		ev.Kind = progress.KindCancelled
		ev.Err = err.Error()
	default:
		ev.Kind = progress.KindError
		ev.Err = err.Error()
	}
	a.emit(ctx, s, ev)
}
