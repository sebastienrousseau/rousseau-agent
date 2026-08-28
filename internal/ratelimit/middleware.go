package ratelimit

import (
	"context"

	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// DefaultDeniedReply is the user-facing string returned when a message
// is dropped by the limiter. Exported so callers can override it.
var DefaultDeniedReply = "You're sending messages too quickly. Try again in a minute."

// Wrap returns a Handler that consults limiter before delegating to
// inner. Denied messages return DeniedReply as a normal reply (nil
// error) so the transport delivers it to the sender. transport is the
// metric label attached to rousseau_ratelimit_denied_total.
func Wrap(inner transport.Handler, limiter *KeyedLimiter, transportName, deniedReply string) transport.Handler {
	if deniedReply == "" {
		deniedReply = DefaultDeniedReply
	}
	return transport.HandlerFunc(func(ctx context.Context, msg transport.IncomingMessage) (string, error) {
		if !limiter.Allow(msg.From) {
			observability.RateLimitDenied.WithLabelValues(transportName).Inc()
			return deniedReply, nil
		}
		return inner.Handle(ctx, msg)
	})
}
