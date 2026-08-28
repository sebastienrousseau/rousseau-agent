// Package resilience implements the panic-recovery middleware and
// per-provider circuit breakers that keep the daemon alive when an
// upstream misbehaves.
//
// Both surfaces are opt-in wrappers rather than baked-in behaviour so
// tests can exercise the raw call paths without middleware side
// effects.
package resilience

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Recover wraps a [transport.Handler] so a panic in the inner Handle
// call is caught, logged, and translated to an error. surface names
// the calling site for the metric label (e.g. "whatsapp", "slack").
//
// The recovered handler returns an "internal error" reply string and
// a non-nil error; the caller (router) decides whether to send the
// reply, log-only, or drop.
func Recover(inner transport.Handler, surface string, logger *slog.Logger) transport.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return transport.HandlerFunc(func(ctx context.Context, msg transport.IncomingMessage) (reply string, err error) {
		defer func() {
			if r := recover(); r != nil {
				observability.PanicsRecovered.WithLabelValues(surface).Inc()
				logger.Error("handler.panic",
					slog.String("surface", surface),
					slog.String("from", msg.From),
					slog.Any("recover", r),
					slog.String("stack", string(debug.Stack())),
				)
				reply = ""
				err = fmt.Errorf("resilience: recovered from panic in %s: %v", surface, r)
			}
		}()
		return inner.Handle(ctx, msg)
	})
}

// RecoverFunc is the func-shaped equivalent of [Recover]. Given the
// call site is often a lambda in daemon assembly, this form avoids a
// throwaway HandlerFunc conversion at the call site.
func RecoverFunc(surface string, logger *slog.Logger, fn transport.HandlerFunc) transport.Handler {
	return Recover(fn, surface, logger)
}
