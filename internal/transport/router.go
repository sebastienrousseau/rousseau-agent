package transport

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
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

// RouterOptions configures a Router.
type RouterOptions struct {
	// Allowlist restricts which sender identifiers may talk to the
	// agent. Empty means anyone may — DO NOT ship this for a production
	// deployment on a public number.
	Allowlist []string
}

// Router binds an inbound Handler to an agent + persistent session state.
// A Router is safe for concurrent use.
type Router struct {
	runner  TurnRunner
	store   SessionStore
	jidMap  JIDMapper
	logger  *slog.Logger
	allow   map[string]struct{}
	openAll bool
	mu      sync.Mutex
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
		runner:  runner,
		store:   store,
		jidMap:  jidMap,
		logger:  logger,
		allow:   allow,
		openAll: len(allow) == 0,
	}
}

// Handle implements Handler.
func (r *Router) Handle(ctx context.Context, msg IncomingMessage) (string, error) {
	if !r.allowed(msg.From) {
		r.logger.Warn("transport.rejected", slog.String("from", msg.From))
		return "", nil
	}

	sess, err := r.sessionFor(ctx, msg.From)
	if err != nil {
		return "", fmt.Errorf("router: session: %w", err)
	}

	sess.Append(agent.NewUserText(msg.Body))
	final, err := r.runner.Turn(ctx, sess)
	if err != nil {
		return "", fmt.Errorf("router: turn: %w", err)
	}
	if err := r.store.Save(ctx, sess); err != nil {
		r.logger.Warn("router.save_failed", slog.String("err", err.Error()))
	}
	return firstText(final), nil
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
