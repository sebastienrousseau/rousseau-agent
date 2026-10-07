package transport

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/approval"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	"github.com/sebastienrousseau/rousseau-agent/internal/identity"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// SessionStore is the subset of state.Store the Router needs. Declared
// here so the transport package does not import internal/state
// concretely for method-level types (though state.Summary in
// ListBySender does require a shallow import).
//
// ListBySender + Delete are on this interface so the
// session-lifecycle chat verbs (/sessions, /delete) work
// driver-agnostically. Both sqlite.Store and postgres.Store
// satisfy this shape.
type SessionStore interface {
	Save(ctx context.Context, s *agent.Session) error
	Load(ctx context.Context, id string) (*agent.Session, error)
	ListBySender(ctx context.Context, sender string, limit int) ([]state.Summary, error)
	SearchBySender(ctx context.Context, sender, query string, opts sqlitestore.SearchOptions) ([]sqlitestore.SearchHit, error)
	Delete(ctx context.Context, id string) error
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
	// SSO, when non-nil, enables the /login + /logout chat commands
	// and consults SSOStore to relax the static Allowlist for
	// SSO-verified senders. Zero-value Nop{} is the safe default —
	// the SSO code paths become inert without requiring nil checks.
	// Wired only when the licence unlocks [license.FeatureSSO]; see
	// [internal/cli/daemon.go] for the assembly point.
	SSO sso.Directory
	// SSOStore persists the (transport, sender) → verified-Identity
	// mapping across restarts. Required whenever SSO is non-nil.
	// Zero-value NoBindings{} is the shipped fallback.
	SSOStore sso.BindingStore
	// SSOBindingTTL bounds how long a /login binding stays valid
	// without re-authentication. Falls back to the token's `exp`
	// claim when zero; the smaller of (TTL, exp) wins. Prevents a
	// mis-issued 1-year token from unlocking a chat identity for
	// a year on the daemon's side.
	SSOBindingTTL time.Duration
	// AuditSink receives one [audit_egress.Record] per /login /
	// /logout attempt (success + failure). Nil disables audit
	// emission entirely — Emit calls simply don't happen. The
	// daemon wires this to the shared enterprise sink assembled
	// in cli/daemon.go.
	AuditSink audit_egress.Sink
	// Approvals is the multi-party approval broker used by the
	// /approve and /deny chat commands. Nil disables both
	// commands. Wired by the daemon when
	// agent.approver.multi_party.rules is non-empty AND the
	// licence unlocks FeatureGovernanceAdvanced.
	Approvals *approval.PendingManager
	// BuildStamp is the string the /version chat command echoes
	// back — build tag, git commit and build date, exactly as
	// `rousseau version` prints them. Populated by the daemon
	// from the ldflag-injected cli package vars. Empty string
	// makes /version reply "unknown build" rather than error, so
	// dev builds without ldflags still answer instead of hanging.
	BuildStamp string
	// SessionIdleTimeout starts a fresh session for a sender whose
	// mapped session has not been updated for longer than this.
	// Without it every inbound resumes the same thread forever, so a
	// terse message days later ("yes") is read as an answer to
	// whatever the agent last asked — with tool permissions that may
	// be bypassed. The old session is kept and named in the reply so
	// /resume can return to it. Zero disables rotation.
	SessionIdleTimeout time.Duration
	// ForkSession, when set, is called by /save with the source and
	// snapshot session ids so a provider that keeps its own copy of
	// the conversation (claudecli's transcript) can duplicate it.
	// Without it, resuming a snapshot on such a provider starts with
	// no history.
	ForkSession func(ctx context.Context, fromID, toID string) error
	// TurnJournal, when set, records each agent turn while it runs so
	// a daemon restarted mid-turn can tell the sender (see
	// sqlite.TurnJournal). Needs Transport.
	TurnJournal TurnJournal
}

// TurnJournal records agent turns in flight.
type TurnJournal interface {
	Begin(ctx context.Context, transport, sender, body string) error
	End(ctx context.Context, transport, sender string) error
}

// Router binds an inbound Handler to an agent + persistent session state.
// A Router is safe for concurrent use.
type Router struct {
	runner     TurnRunner
	store      SessionStore
	jidMap     JIDMapper
	logger     *slog.Logger
	allow      map[string]struct{}
	openAll    bool
	identity   identity.Resolver
	transport  string
	ssoDir     sso.Directory
	ssoStore   sso.BindingStore
	ssoTTL     time.Duration
	auditSink  audit_egress.Sink
	approvals  *approval.PendingManager
	buildStamp string
	idleAfter  time.Duration
	forkSess   func(ctx context.Context, fromID, toID string) error
	journal    TurnJournal
	now        func() time.Time
	// senders serialises session lookup and rebinding per sender; a
	// router-wide mutex used to make every sender wait on one slow
	// store call.
	senders keyedMutex
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
	ssoStore := opts.SSOStore
	if ssoStore == nil {
		// Fail-safe: never let a nil SSOStore panic the router
		// if the caller wires SSO without also wiring the store.
		ssoStore = sso.NoBindings{}
	}
	return &Router{
		runner:     runner,
		store:      store,
		jidMap:     jidMap,
		logger:     logger,
		allow:      allow,
		openAll:    len(allow) == 0,
		identity:   opts.Identity,
		transport:  opts.Transport,
		ssoDir:     opts.SSO,
		ssoStore:   ssoStore,
		ssoTTL:     opts.SSOBindingTTL,
		auditSink:  opts.AuditSink,
		approvals:  opts.Approvals,
		buildStamp: opts.BuildStamp,
		idleAfter:  opts.SessionIdleTimeout,
		forkSess:   opts.ForkSession,
		journal:    opts.TurnJournal,
		now:        time.Now,
	}
}

// emitAuthAudit is the router's nil-safe helper for auth-event
// records (login / logout). Emit errors are swallowed by design
// — auth-audit is best-effort observability and a wedged SIEM
// must not break a /login command mid-flow. Actor is the SSO
// subject on a successful login, or the raw transport sender on
// a failed one (so denial trails still name a target).
func (r *Router) emitAuthAudit(ctx context.Context, verb, actor, from, result string, detail map[string]any) {
	if r.auditSink == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["transport"] = r.transport
	detail["from"] = from
	_ = r.auditSink.Emit(ctx, audit_egress.Record{ //nolint:errcheck // best-effort; sink counters authoritative
		Category: "auth",
		Actor:    actor,
		Verb:     verb,
		Object:   from,
		Result:   result,
		Detail:   detail,
	})
}

// Handle implements Handler.
func (r *Router) Handle(ctx context.Context, msg IncomingMessage) (string, error) {
	// Canonicalise single-letter shortcuts (/c → /clear, /s →
	// /sessions, …) up-front so every downstream dispatch site
	// switches on the canonical form. Keeps the alias table in
	// one place instead of duplicating in every case.
	msg.Body = canonicalCommand(msg.Body)

	// Chat-command interception runs BEFORE the allowlist check so
	// /login can bootstrap a new sender via SSO without the operator
	// having to pre-approve their number. Other commands
	// (/whoami, /link, /unlink) still gate on allow — the identity
	// commands assume you're already inside.
	if r.ssoDir != nil {
		if reply, matched := r.handleSSOCommand(ctx, msg); matched {
			return reply, nil
		}
	}

	if !r.allowed(ctx, msg.From) {
		r.logger.Warn("transport.rejected", slog.String("from", msg.From))
		return "", nil
	}

	// Stash the SSO identity (if any) in ctx so downstream
	// approvers / audit sinks can read it. Independent of the
	// allowlist path — the identity attaches even when a static
	// allowlist did the auth. Lookup filters expired bindings, so
	// we can trust the returned Identity.
	//
	// Runs BEFORE the approval-command intercept so /approve
	// and /deny land with the voter's identity already in ctx.
	if r.ssoStore != nil {
		if id, ok, err := r.ssoStore.Lookup(ctx, r.transport, msg.From); err == nil && ok {
			ctx = sso.WithIdentity(ctx, id)
		}
	}

	// /approve <token> / /deny <token> — multi-party approval
	// votes. Runs after the SSO lookup so the voter's identity
	// is available; the handler enforces that anonymous votes
	// are rejected.
	if r.approvals != nil {
		if reply, matched := r.handleApprovalCommand(ctx, msg); matched {
			return reply, nil
		}
	}

	// /version is unconditionally synchronous — no identity, no
	// storage, no LLM. Handled here BEFORE the identity gate so it
	// works even in deployments that never wired an Identity
	// resolver (which is the daemon's current default). Without
	// this, an operator sending /version watches the message fall
	// through to the LLM and gets a fabricated "Unknown command"
	// reply, defeating the whole point of the verb.
	if strings.TrimSpace(msg.Body) == "/version" {
		return r.cmdVersion(), nil
	}

	// /help is the operator's discovery surface — one place that
	// enumerates every synchronous verb so a new user doesn't
	// have to read source to learn the CLI. Handled here above
	// the identity gate for the same reason /version is: no
	// dependencies, always available.
	if strings.TrimSpace(msg.Body) == "/help" {
		return cmdHelp(), nil
	}

	// /clear starts a fresh session for this sender. The current
	// session stays in the DB (so `rousseau session show <id>`
	// still works) but the jidMap now points at a new empty
	// session — every subsequent inbound builds context from
	// scratch. Same "runs above the identity gate" reason as
	// /version: /clear needs the jidMap + session store, both
	// of which the router always holds.
	//
	// Mid-turn safety: a running turn holds `sess *agent.Session`
	// captured at Handle time. Its later Save writes back to the
	// OLD session ID — untouched by /clear. The fresh session
	// only affects future inbound, which is the operator-visible
	// intent.
	if strings.TrimSpace(msg.Body) == "/clear" {
		reply, err := r.cmdClear(ctx, msg.From)
		if err != nil {
			return "", fmt.Errorf("router: clear: %w", err)
		}
		return reply, nil
	}

	// Session-lifecycle verbs. All keyed on msg.From so they
	// operate on the sender's own sessions only — a listing / rename
	// / resume / delete from Alice can never touch Bob's data.
	// Same "runs above the identity gate" reasoning as /clear.
	if body := strings.TrimSpace(msg.Body); strings.HasPrefix(body, "/") {
		parts := strings.SplitN(body, " ", 2)
		var arg string
		if len(parts) == 2 {
			arg = strings.TrimSpace(parts[1])
		}
		switch parts[0] {
		case "/sessions":
			reply, err := r.cmdSessions(ctx, msg.From)
			if err != nil {
				return "", fmt.Errorf("router: sessions: %w", err)
			}
			return reply, nil
		case "/name":
			reply, err := r.cmdName(ctx, msg.From, arg)
			if err != nil {
				return "", fmt.Errorf("router: name: %w", err)
			}
			return reply, nil
		case "/resume":
			reply, err := r.cmdResume(ctx, msg.From, arg)
			if err != nil {
				return "", fmt.Errorf("router: resume: %w", err)
			}
			return reply, nil
		case "/delete":
			reply, err := r.cmdDelete(ctx, msg.From, arg)
			if err != nil {
				return "", fmt.Errorf("router: delete: %w", err)
			}
			return reply, nil
		case "/save":
			reply, err := r.cmdSave(ctx, msg.From, arg)
			if err != nil {
				return "", fmt.Errorf("router: save: %w", err)
			}
			return reply, nil
		case "/find":
			reply, err := r.cmdFind(ctx, msg.From, arg)
			if err != nil {
				return "", fmt.Errorf("router: find: %w", err)
			}
			return reply, nil
		}
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

	sess, rotatedFrom, err := r.turnSessionFor(ctx, msg.From)
	if err != nil {
		return "", fmt.Errorf("router: session: %w", err)
	}

	if userMsg, ok := buildUserMessage(msg, r.transport); ok {
		sess.Append(userMsg)
		// Persist the sender's message before the turn runs: a turn
		// cut off by a crash, timeout or restart must not lose it.
		r.saveSession(ctx, sess)
	}
	defer r.journalTurn(ctx, msg.From, msg.Body)()
	final, err := r.runTurn(ctx, sess)
	// Save on failure too: tool calls that already ran are side
	// effects the next turn's model must see in the history.
	r.saveSession(ctx, sess)
	if err != nil {
		return "", fmt.Errorf("router: turn: %w", err)
	}
	reply := firstText(final)
	if rotatedFrom != "" {
		reply = fmt.Sprintf("(new session: previous one idle > %s. /resume %s to continue it.)\n\n%s",
			r.idleAfter, shortSessionID(rotatedFrom), reply)
	}
	return reply, nil
}

// journalTurn records that from's turn has started and returns the
// func that records its end. Without a journal both are no-ops.
func (r *Router) journalTurn(ctx context.Context, from, body string) func() {
	if r.journal == nil || r.transport == "" {
		return func() {}
	}
	if err := r.journal.Begin(ctx, r.transport, from, body); err != nil {
		r.logger.Warn("router.journal_failed", slog.String("err", err.Error()))
	}
	return func() {
		// Background: the turn's ctx may already be cancelled
		// (timeout, /cancel), and the record must still clear.
		if err := r.journal.End(context.Background(), r.transport, from); err != nil {
			r.logger.Warn("router.journal_failed", slog.String("err", err.Error()))
		}
	}
}

// saveSession persists sess, detached from ctx's cancellation so a
// cancelled or timed-out turn still records its progress.
func (r *Router) saveSession(ctx context.Context, sess *agent.Session) {
	if err := r.store.Save(context.WithoutCancel(ctx), sess); err != nil {
		r.logger.Warn("router.save_failed", slog.String("err", err.Error()))
	}
}

// syncCommands lists every leading token the router answers
// without touching the LLM. Kept in one place so IsSyncCommand
// stays in lockstep with the Handle-time dispatch — a new
// synchronous verb needs one entry here plus its handler case.
//
// Groupings (comment only, not enforced):
//   - identity:  /whoami /link /unlink /version
//   - SSO:       /login /logout
//   - approvals: /approve /deny
var syncCommands = map[string]struct{}{
	// Every verb has a shortcut — the alias table below owns
	// the canonical-form mapping. This map only needs to
	// declare membership so the Supervisor's SyncPeeker
	// recognises both forms as sync commands and bypasses
	// steering.
	"/whoami":   {},
	"/w":        {}, // shortcut for /whoami
	"/link":     {},
	"/lk":       {}, // shortcut for /link (/l would collide with /login)
	"/unlink":   {},
	"/ul":       {}, // shortcut for /unlink
	"/version":  {},
	"/v":        {}, // shortcut for /version
	"/help":     {},
	"/h":        {}, // shortcut for /help
	"/clear":    {},
	"/c":        {}, // shortcut for /clear
	"/sessions": {},
	"/ls":       {}, // shortcut for /sessions (shell muscle memory —
	// /s is Ctrl+S save)
	"/name":   {},
	"/n":      {}, // shortcut for /name
	"/resume": {},
	"/r":      {}, // shortcut for /resume — dual use: alone unpauses a
	// running turn (control verb, exact-match), with a
	// short-id switches sessions (router). Same shortcut
	// works for both because canonicalCommand normalises
	// /r → /resume before control.Decide sees it.
	"/delete": {},
	"/d":      {}, // shortcut for /delete
	"/save":   {},
	"/s":      {}, // shortcut for /save (Ctrl+S muscle memory).
	// /ls above lists sessions instead.
	"/find":    {},
	"/f":       {}, // shortcut for /find (search across your sessions)
	"/rm":      {}, // shell-alias for /delete
	"/login":   {},
	"/li":      {}, // shortcut for /login
	"/logout":  {},
	"/lo":      {}, // shortcut for /logout
	"/approve": {},
	"/ap":      {}, // shortcut for /approve
	"/deny":    {},
	"/ny":      {}, // shortcut for /deny (/d is taken by /delete)
}

// commandAliases collapses shortcuts into their canonical verb
// so the Handle-time dispatch only has to switch on one form.
// Every verb has an entry here — see cmdHelp for the full
// operator-facing listing.
//
// The scheme:
//   - single-letter shortcuts for the highest-frequency verbs
//     (/v /h /c /s /n /r /d /w)
//   - two-letter shortcuts for verbs whose first letter is
//     already taken (/lk /ul /li /lo /ap /ny)
//   - shell-metaphor aliases (/ls /rm) as bonuses
var commandAliases = map[string]string{
	"/w":  "/whoami",
	"/lk": "/link",
	"/ul": "/unlink",
	"/v":  "/version",
	"/h":  "/help",
	"/c":  "/clear",
	"/s":  "/save", // Ctrl+S muscle memory wins over "s = sessions"
	"/f":  "/find",
	"/n":  "/name",
	"/r":  "/resume",
	"/d":  "/delete",
	"/ls": "/sessions", // shell muscle memory instead
	"/rm": "/delete",
	"/li": "/login",
	"/lo": "/logout",
	"/ap": "/approve",
	"/ny": "/deny",
}

// canonicalCommand returns the canonical form of the leading
// token in body. Returns the input unchanged when body is not
// a slash command or the token is not an alias.
func canonicalCommand(body string) string {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "/") {
		return body
	}
	parts := strings.SplitN(trimmed, " ", 2)
	if canon, ok := commandAliases[parts[0]]; ok {
		if len(parts) == 2 {
			return canon + " " + parts[1]
		}
		return canon
	}
	return body
}

// IsSyncCommand reports whether msg would be answered by one of
// the router's synchronous handlers rather than reaching the LLM.
// Satisfies [SyncPeeker] so [Supervisor.Wrap] bypasses steer/
// begin for these — otherwise a /version arriving during a
// running turn would be folded into the turn as prompt text
// instead of returning the build stamp.
//
// Only inspects the leading token — it is a peek, not a
// permission decision. Actual dispatch (allowlist, SSO stash,
// per-handler validation) still happens in Handle. An
// unauthorised sender's /whoami will bypass steer, reach Handle,
// and be rejected there — the same outcome as when no turn is
// running.
func (r *Router) IsSyncCommand(msg IncomingMessage) bool {
	body := strings.TrimSpace(msg.Body)
	if !strings.HasPrefix(body, "/") {
		return false
	}
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return false
	}
	_, ok := syncCommands[parts[0]]
	return ok
}
