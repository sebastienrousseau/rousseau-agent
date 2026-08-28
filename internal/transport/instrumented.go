package transport

import (
	"context"

	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
)

// InstrumentedHandler wraps h to record inbound + outbound counters on
// the observability registry. Each transport client wraps the router
// with this at daemon assembly time, so metrics carry the transport
// name without every transport client having to know about the
// observability package.
func InstrumentedHandler(name string, h Handler) Handler {
	return HandlerFunc(func(ctx context.Context, msg IncomingMessage) (string, error) {
		observability.TransportIncoming.WithLabelValues(name).Inc()
		reply, err := h.Handle(ctx, msg)
		status := "ok"
		if err != nil {
			status = "error"
		}
		observability.TransportOutgoing.WithLabelValues(name, status).Inc()
		return reply, err
	})
}
