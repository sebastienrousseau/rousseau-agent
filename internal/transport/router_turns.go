package transport

import (
	"context"
	"log/slog"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/senderkey"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

// Session selection and turn execution.

func (r *Router) sessionFor(ctx context.Context, jid string) (*agent.Session, error) {
	defer r.senders.Lock(jid)()

	id, ok, err := r.jidMap.Get(ctx, r.key(jid))
	if err != nil {
		return nil, err
	}
	if ok {
		sess, err := r.store.Load(ctx, id)
		if err == nil {
			return sess, nil
		}
		// Fall through: mapping is stale; create a new session.
		r.logger.Warn("router.stale_mapping", slog.String("jid", jid), slog.String("err", err.Error()))
	}
	return r.newSessionLocked(ctx, jid)
}

// turnSessionFor is sessionFor for the conversational path: when
// idle rotation is enabled and the mapped session was last updated
// more than idleAfter ago, it binds the sender to a fresh session
// and reports the previous ID so the reply can point at /resume.
// Verbs that act on "the current session" (/name, /save) keep using
// sessionFor so they never rotate as a side effect.
func (r *Router) turnSessionFor(ctx context.Context, jid string) (*agent.Session, string, error) {
	if r.idleAfter <= 0 {
		sess, err := r.sessionFor(ctx, jid)
		return sess, "", err
	}
	defer r.senders.Lock(jid)()

	id, ok, err := r.jidMap.Get(ctx, r.key(jid))
	if err != nil {
		return nil, "", err
	}
	if ok {
		sess, err := r.store.Load(ctx, id)
		if err == nil {
			idle := r.now().Sub(sess.UpdatedAt)
			if idle <= r.idleAfter {
				return sess, "", nil
			}
			fresh, err := r.newSessionLocked(ctx, jid)
			if err != nil {
				return nil, "", err
			}
			r.logger.Info("router.session_idle_rotated",
				slog.String("from", jid),
				slog.String("previous_session_id", sess.ID),
				slog.String("new_session_id", fresh.ID),
				slog.Duration("idle", idle.Round(time.Second)))
			return fresh, sess.ID, nil
		}
		r.logger.Warn("router.stale_mapping", slog.String("jid", jid), slog.String("err", err.Error()))
	}
	sess, err := r.newSessionLocked(ctx, jid)
	return sess, "", err
}

// newSessionLocked creates, persists and binds a fresh session for
// jid. Caller holds jid's lock in r.senders.
func (r *Router) newSessionLocked(ctx context.Context, jid string) (*agent.Session, error) {
	sess := agent.NewSession("chat: " + jid)
	sess.Sender = r.key(jid) // enables /sessions to list this session for the sender later
	if err := r.store.Save(ctx, sess); err != nil {
		return nil, err
	}
	if err := r.jidMap.Put(ctx, r.key(jid), sess.ID); err != nil {
		return nil, err
	}
	return sess, nil
}

func firstText(m agent.Message) string {
	for _, c := range m.Content {
		if c.Kind == agent.ContentText && c.Text != "" {
			return c.Text
		}
	}
	return ""
}

// runTurn is the streaming-aware dispatch. It uses TurnStream when
// the runner supports it AND the context carries a progress
// publisher (typically installed by transport.Supervisor's per-turn
// Registry.Begin) — that combination is what makes the per-tool
// progress feed reach a transport reporter. Otherwise it falls back
// to the plain Turn, keeping the code path used by every
// non-supervised call site (cron scheduler, embedded API,
// integration tests) unchanged.
//
// The StreamEvent channel is a discard drain: the router itself only
// cares about the final Message, but the underlying agent stream
// path insists on a place to send events so the provider goroutine
// does not block. Draining in a small goroutine keeps the memory
// footprint bounded to one buffered slot per event.
//
// Channel-close ownership: TurnStream is the sender and closes
// events itself (`defer close(events)` in stream_turn.go's exported
// entry point). runTurn does NOT close it — a second close would
// panic ("close of closed channel"). The drain goroutine's range
// loop exits naturally when TurnStream closes the channel, and the
// <-drained wait synchronises with that exit.
func (r *Router) runTurn(ctx context.Context, sess *agent.Session) (agent.Message, error) {
	streamer, ok := r.runner.(StreamingTurnRunner)
	if !ok || progress.PublisherFrom(ctx) == nil {
		return r.runner.Turn(ctx, sess)
	}
	events := make(chan agent.StreamEvent, 16)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		// Drain the channel — the useful copy of every event lives on the
		// progress bus; this loop just prevents TurnStream from blocking
		// on a full buffer.
		for range events {
		}
	}()
	final, err := streamer.TurnStream(ctx, sess, events)
	<-drained
	return final, err
}

// buildUserMessage folds the inbound text plus every downloaded
// attachment into a single user Message. Returns ok=false when the
// message has neither text nor attachments — the caller should skip
// the Append+Turn round-trip because there is nothing to say. The
// transport tag records the source (whatsapp / signal / …) on each
// image so downstream audit trails and per-provider adapters know
// where the bytes came from.
func buildUserMessage(msg IncomingMessage, transport string) (agent.Message, bool) {
	contents := make([]agent.Content, 0, 1+len(msg.Attachments))
	if msg.Body != "" {
		contents = append(contents, agent.Content{Kind: agent.ContentText, Text: msg.Body})
	}
	for _, att := range msg.Attachments {
		if len(att.Data) == 0 {
			continue
		}
		contents = append(contents, agent.Content{
			Kind: agent.ContentImage,
			Image: &agent.Image{
				MediaType: att.MediaType,
				Data:      att.Data,
				Source:    transport,
			},
		})
	}
	if len(contents) == 0 {
		return agent.Message{}, false
	}
	return agent.Message{
		Role:      agent.RoleUser,
		Content:   contents,
		CreatedAt: time.Now().UTC(),
	}, true
}

// key is the stored form of a sender on this router's transport:
// "<transport>:<from>", so senders on different transports never
// share sessions, /sessions lists or /find results.
func (r *Router) key(from string) string { return senderkey.Make(r.transport, from) }
