package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/identity"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

// SessionStore is the subset of state.Store the Router needs. Declared
// here so the transport package does not import internal/state
// concretely.
type SessionStore interface {
	Save(ctx context.Context, s *agent.Session) error
	Load(ctx context.Context, id string) (*agent.Session, error)
}

// JIDMapper persists which agent Session belongs to which platform
// sender.
type JIDMapper interface {
	// Get returns the sessionID mapped to jid, or ok=false.
	Get(ctx context.Context, jid string) (sessionID string, ok bool, err error)
	// Put records the mapping jid → sessionID.
	Put(ctx context.Context, jid, sessionID string) error
}

// TurnRunner runs a single agent turn against a Session.
type TurnRunner interface {
	Turn(ctx context.Context, s *agent.Session) (agent.Message, error)
}

// StreamingTurnRunner is the OPTIONAL streaming twin of TurnRunner.
// Runners that also implement this let the Router unlock the
// progress-event flow: each provider tool_use / text_delta emitted
// by TurnStream reaches agent.emit(ctx, ...) → the context publisher
// installed by transport.Supervisor → the transport's progress Bus →
// the WhatsApp reporter → live message edits.
//
// Kept as a separate optional interface so mock TurnRunners in
// existing tests (and any non-streaming runner) keep compiling
// without emitting the extra method.
type StreamingTurnRunner interface {
	TurnStream(ctx context.Context, s *agent.Session, events chan<- agent.StreamEvent) (agent.Message, error)
}

// RouterOptions configures a Router.
type RouterOptions struct {
	// Allowlist restricts which sender identifiers may talk to the
	// agent. Empty means anyone may — DO NOT ship this for a production
	// deployment on a public number.
	Allowlist []string
	// Identity, when non-nil, enables cross-transport session
	// continuity. Every inbound message's (Transport, msg.From) pair
	// is resolved to an identity ID via the resolver; that ID becomes
	// the session key. Unlinked senders auto-provision a fresh
	// identity on their first message. Nil preserves the legacy
	// per-JID session model — backwards compatible with pre-v0.0.2
	// deployments.
	Identity identity.Resolver
	// Transport is the name of the transport ("whatsapp", "slack", …)
	// this Router is bound to. Ignored when Identity is nil; required
	// when Identity is set (to form the (transport, sender) tuple the
	// resolver keys on).
	Transport string
}

// Router binds an inbound Handler to an agent + persistent session state.
// A Router is safe for concurrent use.
type Router struct {
	runner    TurnRunner
	store     SessionStore
	jidMap    JIDMapper
	logger    *slog.Logger
	allow     map[string]struct{}
	openAll   bool
	identity  identity.Resolver
	transport string
	mu        sync.Mutex
}

// NewRouter constructs a Router. The runner performs each Turn; store
// persists the Session; jidMap remembers which Session belongs to which
// sender.
func NewRouter(runner TurnRunner, store SessionStore, jidMap JIDMapper, logger *slog.Logger, opts RouterOptions) *Router {
	if logger == nil {
		logger = slog.Default()
	}
	allow := map[string]struct{}{}
	for _, id := range opts.Allowlist {
		allow[id] = struct{}{}
	}
	return &Router{
		runner:    runner,
		store:     store,
		jidMap:    jidMap,
		logger:    logger,
		allow:     allow,
		openAll:   len(allow) == 0,
		identity:  opts.Identity,
		transport: opts.Transport,
	}
}

// Handle implements Handler.
func (r *Router) Handle(ctx context.Context, msg IncomingMessage) (string, error) {
	if !r.allowed(msg.From) {
		r.logger.Warn("transport.rejected", slog.String("from", msg.From))
		return "", nil
	}

	// Chat-command interception: /whoami, /link, /unlink handled
	// before the LLM sees the message so they run instantly + free.
	// Only fires when an Identity resolver is configured — the
	// commands are meaningless without it.
	if r.identity != nil {
		if reply, matched := r.handleIdentityCommand(ctx, msg); matched {
			return reply, nil
		}
	}

	sess, err := r.sessionFor(ctx, msg.From)
	if err != nil {
		return "", fmt.Errorf("router: session: %w", err)
	}

	if userMsg, ok := buildUserMessage(msg, r.transport); ok {
		sess.Append(userMsg)
	}
	final, err := r.runTurn(ctx, sess)
	if err != nil {
		return "", fmt.Errorf("router: turn: %w", err)
	}
	if err := r.store.Save(ctx, sess); err != nil {
		r.logger.Warn("router.save_failed", slog.String("err", err.Error()))
	}
	return firstText(final), nil
}

// handleIdentityCommand recognises the three identity-management
// commands and returns the reply text. Second return value indicates
// whether the message was a command (true → don't fall through to
// the LLM).
func (r *Router) handleIdentityCommand(ctx context.Context, msg IncomingMessage) (string, bool) {
	body := strings.TrimSpace(msg.Body)
	if body == "" || !strings.HasPrefix(body, "/") {
		return "", false
	}
	parts := strings.Fields(body)
	switch parts[0] {
	case "/whoami":
		return r.cmdWhoami(ctx, msg.From), true
	case "/link":
		if len(parts) != 2 || !strings.Contains(parts[1], ":") {
			return "usage: /link <transport>:<sender>", true
		}
		tp, sender, _ := strings.Cut(parts[1], ":")
		return r.cmdLink(ctx, msg.From, tp, sender), true
	case "/unlink":
		if len(parts) != 2 || !strings.Contains(parts[1], ":") {
			return "usage: /unlink <transport>:<sender>", true
		}
		tp, sender, _ := strings.Cut(parts[1], ":")
		return r.cmdUnlink(ctx, tp, sender), true
	}
	return "", false
}

func (r *Router) cmdWhoami(ctx context.Context, from string) string {
	id, err := r.resolveOrProvision(ctx, from, from)
	if err != nil {
		return "whoami: " + err.Error()
	}
	rec, err := r.identity.Get(ctx, id)
	if err != nil {
		return "whoami: " + err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "identity: %s\n", rec.ID)
	if rec.PrimaryDisplay != "" {
		fmt.Fprintf(&b, "display:  %s\n", rec.PrimaryDisplay)
	}
	fmt.Fprintf(&b, "handles:  %d\n", len(rec.Handles))
	for _, h := range rec.Handles {
		fmt.Fprintf(&b, "  %s:%s\n", h.Transport, h.Sender)
	}
	return b.String()
}

func (r *Router) cmdLink(ctx context.Context, from, tp, sender string) string {
	id, err := r.resolveOrProvision(ctx, from, from)
	if err != nil {
		return "link: " + err.Error()
	}
	if err := r.identity.Link(ctx, id, tp, sender); err != nil {
		return "link: " + err.Error()
	}
	return fmt.Sprintf("linked %s:%s to identity %s", tp, sender, id)
}

func (r *Router) cmdUnlink(ctx context.Context, tp, sender string) string {
	if err := r.identity.Unlink(ctx, tp, sender); err != nil {
		return "unlink: " + err.Error()
	}
	return fmt.Sprintf("unlinked %s:%s", tp, sender)
}

// resolveOrProvision looks up the identity for (transport, from) and
// auto-creates one on first sight. Called both by the session lookup
// path and by the /whoami command.
func (r *Router) resolveOrProvision(ctx context.Context, from, display string) (identity.ID, error) {
	id, err := r.identity.Resolve(ctx, r.transport, from)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, identity.ErrNotLinked) {
		return "", err
	}
	return r.identity.Provision(ctx, r.transport, from, display)
}

func (r *Router) allowed(from string) bool {
	if r.openAll {
		return true
	}
	_, ok := r.allow[from]
	return ok
}

func (r *Router) sessionFor(ctx context.Context, jid string) (*agent.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id, ok, err := r.jidMap.Get(ctx, jid)
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
	sess := agent.NewSession("chat: " + jid)
	if err := r.store.Save(ctx, sess); err != nil {
		return nil, err
	}
	if err := r.jidMap.Put(ctx, jid, sess.ID); err != nil {
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
