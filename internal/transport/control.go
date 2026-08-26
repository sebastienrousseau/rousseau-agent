package transport

import (
	"context"
	"log/slog"

	"github.com/sebastienrousseau/rousseau-agent/internal/control"
)

// SteerAck is the reply sent when a mid-flight message is folded into
// the turn already running instead of starting a new one.
const SteerAck = "Noted — folding that into the run in progress. (/status to see where I'm at, /cancel to stop.)"

// Supervisor is the inbound-side half of mid-flight interaction. It
// sits between the transport and the Router and decides, for every
// message, one of three things:
//
//  1. it is a control verb aimed at a turn already running — answer it
//     from the registry, instantly and without touching the LLM;
//  2. a turn is already running — steer the text into it, because
//     starting a second concurrent turn would race on the Session's
//     message list and queueing it would leave the user waiting
//     minutes for an acknowledgement;
//  3. nothing is running — register a new turn and run it.
//
// A Supervisor is safe for concurrent use.
type Supervisor struct {
	reg    *control.Registry
	logger *slog.Logger
}

// NewSupervisor constructs a Supervisor over reg.
func NewSupervisor(reg *control.Registry, logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{reg: reg, logger: logger}
}

// Registry exposes the underlying turn registry.
func (s *Supervisor) Registry() *control.Registry { return s.reg }

// Wrap returns a Handler that applies the supervision rules before
// delegating to next.
func (s *Supervisor) Wrap(next Handler) Handler {
	return HandlerFunc(func(ctx context.Context, msg IncomingMessage) (string, error) {
		key := msg.From
		d := control.Decide(msg.Body)
		if d.IsControl() {
			s.logger.Info("transport.control",
				slog.String("from", key),
				slog.String("verb", string(d.Verb)))
			return s.reg.Apply(d, key), nil
		}
		if d.Prompt == "" {
			return "", nil
		}
		if turn, ok := s.reg.Lookup(key); ok && turn.Steer(d.Prompt) {
			s.logger.Info("transport.steered", slog.String("from", key))
			return SteerAck, nil
		}
		// Either nothing was running, or the turn finished in the gap
		// between Lookup and Steer — run this message as a fresh turn.
		tctx, turn := s.reg.Begin(ctx, key)
		defer turn.End()
		msg.Body = d.Prompt
		return next.Handle(tctx, msg)
	})
}
