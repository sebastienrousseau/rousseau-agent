package transport

import (
	"context"
	"log/slog"
	"strings"

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

// beginRetryBudget bounds Wrap's steer-vs-begin retry loop. Each miss
// costs a map lookup and either a Turn.Steer under one mutex or a
// TryBegin under another, so the ceiling is generous: pathological
// contention cannot spin forever, but a normal collision between two
// concurrent inbounds resolves in one or two hops.
//
// Kept as a var (not const) so a test can lower it to exercise the
// exhaustion warn path without staging a real race.
var beginRetryBudget = 8

// maxFollowUps bounds how many follow-up turns one claimed turn runs
// for steered text the provider did not consume.
const maxFollowUps = 4

// followUps answers text that was steered into the turn but never
// consumed (claudecli runs a whole turn itself, so the agent's steering
// checkpoint never sees it). Each batch runs as another turn under the
// same claim, so more messages can keep steering in, and its reply is
// appended. Without this the sender was told "Noted — folding that
// in…" and the message was dropped.
func (s *Supervisor) followUps(ctx context.Context, next Handler, turn *control.Turn, msg IncomingMessage, reply string, err error) (string, error) {
	for i := 0; err == nil && i < maxFollowUps; i++ {
		left := turn.DrainOrFinish()
		if len(left) == 0 {
			return reply, nil
		}
		s.logger.Info("transport.steer_followup",
			slog.String("from", msg.From),
			slog.Int("messages", len(left)))
		fu := msg
		fu.Body = strings.Join(left, "\n\n")
		fu.Attachments = nil
		more, ferr := next.Handle(ctx, fu)
		if ferr != nil {
			return reply, ferr
		}
		switch {
		case reply == "":
			reply = more
		case more != "":
			reply += "\n\n" + more
		}
	}
	return reply, err
}

// SyncPeeker is the optional capability a downstream Handler
// implements to opt into "answer this synchronously, do not
// steer" behaviour. When Wrap's next handler satisfies this
// interface, Wrap calls IsSyncCommand before touching the turn
// registry — the handler returns true for messages it can answer
// without running a turn (e.g. /whoami, /version, /link, /unlink),
// or false for anything else.
//
// Without this, a chat message like /version arriving during a
// running turn is steered — the "/version" text becomes prompt
// content fed to the running LLM, and the operator never sees
// the build stamp they asked for. When IsSyncCommand returns
// true Wrap calls next.Handle directly, letting the router
// dispatch to its existing synchronous handlers without any
// duplicated logic in the supervisor.
type SyncPeeker interface {
	IsSyncCommand(msg IncomingMessage) bool
}

// Wrap returns a Handler that applies the supervision rules before
// delegating to next.
//
// The retry loop is what makes concurrent inbounds safe. Two messages
// for the same key can race: both may see "nothing running" on Lookup,
// both may then call TryBegin, and TryBegin serialises them — one
// begins, the other collides. The loser retries: on the next hop
// Lookup finds the winner's turn and folds the loser's text in via
// Steer, so the LLM sees one turn that received two messages instead
// of two turns racing on the same claude session.
//
// When next implements SyncPeeker, Wrap consults IsSyncCommand
// first so downstream-synchronous chat commands (identity verbs,
// /version, SSO, approval votes) answer immediately regardless of
// turn state — instead of being steered into a running turn as
// prompt text.
func (s *Supervisor) Wrap(next Handler) Handler {
	peeker, _ := next.(SyncPeeker)
	return HandlerFunc(func(ctx context.Context, msg IncomingMessage) (string, error) {
		// Keyed by conversation so a group message never steers into
		// the sender's running DM turn, or the other way round.
		key := ConversationKey(msg)
		d := control.Decide(msg.Body)
		if d.IsControl() {
			s.logger.Info("transport.control",
				slog.String("from", key),
				slog.String("verb", string(d.Verb)))
			return s.reg.Apply(d, key), nil
		}
		if peeker != nil && peeker.IsSyncCommand(msg) {
			s.logger.Info("transport.sync_command", slog.String("from", key))
			return next.Handle(ctx, msg)
		}
		if d.Prompt == "" {
			return "", nil
		}
		for attempt := 0; attempt < beginRetryBudget; attempt++ {
			if turn, ok := s.reg.Lookup(key); ok && turn.Steer(d.Prompt) {
				s.logger.Info("transport.steered", slog.String("from", key))
				return SteerAck, nil
			}
			tctx, turn, claimed := s.reg.TryBegin(ctx, key)
			if claimed {
				defer turn.End()
				msg.Body = d.Prompt
				reply, err := next.Handle(tctx, msg)
				return s.followUps(tctx, next, turn, msg, reply, err)
			}
			// A concurrent inbound claimed the key first. Loop back to
			// steer into their turn.
		}
		s.logger.Warn("transport.begin_retry_exhausted",
			slog.String("from", key),
			slog.Int("attempts", beginRetryBudget))
		return "", nil
	})
}
