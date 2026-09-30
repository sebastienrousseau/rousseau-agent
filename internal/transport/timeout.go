package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// WithTurnTimeout bounds each turn h runs to d. Without it a hung
// provider (a claude child that never exits) holds its sender's turn
// forever: nothing else from that sender is answered and /cancel is
// the only way out. On expiry the context is cancelled, which stops
// the provider's subprocess, and the sender gets a plain explanation
// rather than silence. Cancellation from the caller (/cancel,
// shutdown) passes through unchanged. d <= 0 disables the deadline.
func WithTurnTimeout(h Handler, d time.Duration, logger *slog.Logger) Handler {
	if d <= 0 {
		return h
	}
	if logger == nil {
		logger = slog.Default()
	}
	return HandlerFunc(func(ctx context.Context, msg IncomingMessage) (string, error) {
		tctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		reply, err := h.Handle(tctx, msg)
		if ctx.Err() == nil && errors.Is(tctx.Err(), context.DeadlineExceeded) {
			logger.Warn("transport.turn_timeout",
				slog.String("from", msg.From),
				slog.Duration("limit", d))
			return fmt.Sprintf("stopped: this turn ran longer than %s (agent.turn_timeout). "+
				"Some steps may already have run; ask me what state things are in before retrying.", d), nil
		}
		return reply, err
	})
}

// TurnLimiter caps how many turns run at once across every handler it
// wraps. Each agent turn on the claudecli backend is its own claude
// process (a few hundred MB), and WhatsApp starts one goroutine per
// inbound message, so without a cap a burst of senders means a burst
// of processes. Excess turns wait for a slot; a queued turn gives up
// when its context ends.
type TurnLimiter struct {
	slots  chan struct{}
	logger *slog.Logger
}

// NewTurnLimiter returns a limiter allowing n concurrent turns, or nil
// (no limit) when n <= 0. A nil *TurnLimiter's Wrap is the identity.
func NewTurnLimiter(n int, logger *slog.Logger) *TurnLimiter {
	if n <= 0 {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &TurnLimiter{slots: make(chan struct{}, n), logger: logger}
}

// Wrap bounds h by the limiter's shared slots.
func (l *TurnLimiter) Wrap(h Handler) Handler {
	if l == nil {
		return h
	}
	return HandlerFunc(func(ctx context.Context, msg IncomingMessage) (string, error) {
		select {
		case l.slots <- struct{}{}:
		default:
			l.logger.Info("transport.turn_queued", slog.String("from", msg.From), slog.Int("limit", cap(l.slots)))
			select {
			case l.slots <- struct{}{}:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		defer func() { <-l.slots }()
		return h.Handle(ctx, msg)
	})
}

// WithConcurrencyLimit wraps h with a limiter of its own; see
// TurnLimiter. n <= 0 disables the cap.
func WithConcurrencyLimit(h Handler, n int, logger *slog.Logger) Handler {
	return NewTurnLimiter(n, logger).Wrap(h)
}
