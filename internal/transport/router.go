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
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/approval"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	"github.com/sebastienrousseau/rousseau-agent/internal/identity"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
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
	mu         sync.Mutex
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

// handleIdentityCommand recognises the router's synchronous chat
// commands — identity (/whoami, /link, /unlink) plus the build-stamp
// echo (/version) — and returns the reply text. Second return value
// indicates whether the message was a command (true → don't fall
// through to the LLM).
func (r *Router) handleIdentityCommand(ctx context.Context, msg IncomingMessage) (string, bool) {
	body := strings.TrimSpace(msg.Body)
	if body == "" || !strings.HasPrefix(body, "/") {
		return "", false
	}
	parts := strings.Fields(body)
	switch parts[0] {
	case "/whoami":
		return r.cmdWhoami(ctx, msg.From), true
	case "/version":
		return r.cmdVersion(), true
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

// cmdVersion answers the /version chat command with the daemon's
// build stamp. Never touches the LLM, never touches storage — the
// only purpose is to let an operator prove from any chat client
// which binary is answering (post-redeploy sanity check).
//
// Deliberately does NOT surface uptime or PID: those are host
// concerns and can be answered by `podman inspect` / `systemctl
// status`. The chat channel's job is to prove the code version,
// not to be a shell.
func (r *Router) cmdVersion() string {
	if r.buildStamp == "" {
		return "rousseau (unknown build — daemon started without a build stamp)"
	}
	return "rousseau " + r.buildStamp
}

// cmdClear starts a fresh session for the sender. The old
// session is NOT deleted — it stays in the DB so
// `rousseau session show <id>` and audit / cost queries still
// work — but the jidMap now points at a new empty session, so
// every subsequent inbound builds LLM context from scratch.
//
// Idempotent: /clear on a sender with no existing session still
// provisions a fresh one and returns success. Operators can
// re-clear without needing to check state first.
//
// Mid-turn race note: a turn started before /clear holds a
// pointer to the OLD session and continues writing to it. That
// is intentional — the running work isn't interrupted, and the
// user's next message picks up the empty new session. Users
// wanting to kill an in-flight turn should use /cancel first.
func (r *Router) cmdClear(ctx context.Context, from string) (string, error) {
	sess := agent.NewSession("chat: " + from)
	sess.Sender = from // so it surfaces in /sessions later
	if err := r.store.Save(ctx, sess); err != nil {
		return "", fmt.Errorf("save fresh session: %w", err)
	}
	// Overwrites the previous mapping via Put's upsert
	// semantics. Both driver implementations expose this as
	// ON CONFLICT DO UPDATE — no delete step needed.
	if err := r.jidMap.Put(ctx, from, sess.ID); err != nil {
		return "", fmt.Errorf("rebind jid: %w", err)
	}
	r.logger.Info("router.session_cleared",
		slog.String("from", from),
		slog.String("new_session_id", sess.ID),
	)
	// Reply is deliberately explicit about scope: /clear resets
	// my memory of the conversation (a DB record), not the chat
	// bubbles in your WhatsApp thread — those live on your phone
	// and only you can remove them.
	return "conversation cleared from my memory. next message starts a fresh session.\n(your WhatsApp chat bubbles stay — I can't reach your phone's chat history.)", nil
}

// cmdSessions lists the sender's sessions newest-first with a
// number, friendly title, message count, short-id, and a short
// preview snippet of the last user turn. The short-id is what
// /resume + /delete accept — numbers are display-only (stateful
// "last-listed cache" would race under concurrent inbounds;
// short-id is stateless).
//
// Preview is drawn from the LAST user message so operators
// recognise "the deploy session" vs "the compliance session"
// at a glance without /resume-then-scroll. Loaded via
// store.Load per entry — capped at listCap so worst-case cost
// stays bounded (~20 queries) and stays off the wire when
// a sender has hundreds of sessions.
func (r *Router) cmdSessions(ctx context.Context, from string) (string, error) {
	const listCap = 20
	summaries, err := r.store.ListBySender(ctx, from, listCap)
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}
	if len(summaries) == 0 {
		return "no saved sessions yet. any message you send here starts one — use /name \"…\" to give it a memorable label.", nil
	}
	current, _, _ := r.jidMap.Get(ctx, from) //nolint:errcheck // "unknown" is a valid state → current == ""
	var b strings.Builder
	fmt.Fprintf(&b, "sessions (newest first, up to %d):\n", listCap)
	for i, s := range summaries {
		marker := " "
		if s.ID == current {
			marker = "*"
		}
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(&b, "%s %2d. %s  (%d msg)  %s\n",
			marker, i+1, title, s.MessageCount, shortSessionID(s.ID))
		if preview := r.sessionPreview(ctx, s.ID); preview != "" {
			fmt.Fprintf(&b, "    ↳ %s\n", preview)
		}
	}
	b.WriteString("\n* = current • /resume <shortid> to switch • /delete <shortid> to remove • /name \"…\" to rename")
	return b.String(), nil
}

// sessionPreview returns a short snippet of the most recent user
// message on a session — used by /sessions so operators can tell
// their sessions apart at a glance. Failures are swallowed
// (empty string) because a missing preview must never break
// the whole listing; the id / title / count are still useful
// on their own.
func (r *Router) sessionPreview(ctx context.Context, sessionID string) string {
	sess, err := r.store.Load(ctx, sessionID)
	if err != nil || sess == nil {
		return ""
	}
	// Walk messages from the tail so multi-turn sessions surface
	// the freshest user question, not the opener.
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role != agent.RoleUser {
			continue
		}
		for _, blk := range sess.Messages[i].Content {
			if blk.Kind != agent.ContentText {
				continue
			}
			text := strings.TrimSpace(blk.Text)
			if text == "" {
				continue
			}
			return truncatePreview(text, 80)
		}
	}
	return ""
}

// truncatePreview clips text to n runes and appends "…" if it
// was truncated. Newlines collapse to spaces so previews stay
// single-line in the /sessions grid. Rune-aware so a multi-byte
// UTF-8 codepoint never splits mid-character (a 2-byte emoji
// truncated at the byte boundary would render as U+FFFD).
func truncatePreview(text string, n int) string {
	// Collapse whitespace runs so \n\n and tabs don't inflate the
	// snippet or leave awkward blank lines mid-listing.
	fields := strings.Fields(text)
	text = strings.Join(fields, " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n]) + "…"
}

// cmdName renames the CURRENT session (the one jidMap points
// at). Empty name → shows the current name plus usage help,
// which is more discoverable than a bare usage line for
// operators who type /name to check what session they're in.
// A rename is a pure Title update — the message history +
// session ID are untouched, so downstream references
// (`rousseau session show <id>`, cost queries) keep working.
func (r *Router) cmdName(ctx context.Context, from, name string) (string, error) {
	name = trimQuotes(strings.TrimSpace(name))
	sess, err := r.sessionFor(ctx, from)
	if err != nil {
		return "", fmt.Errorf("session for: %w", err)
	}
	if name == "" {
		current := sess.Title
		if current == "" {
			current = "(untitled)"
		}
		r.logger.Info("router.name_shown_current",
			slog.String("from", from),
			slog.String("current_title", current),
		)
		return fmt.Sprintf("current session name: %q\nusage: /name \"new name\"  (or /n for short)", current), nil
	}
	sess.Title = name
	if err := r.store.Save(ctx, sess); err != nil {
		return "", fmt.Errorf("save renamed session: %w", err)
	}
	r.logger.Info("router.session_renamed",
		slog.String("from", from),
		slog.String("session_id", sess.ID),
		slog.String("name", name))
	return fmt.Sprintf("renamed current session → %q", name), nil
}

// cmdResume repoints the sender's jidMap entry at the target
// session so subsequent inbound builds LLM context from that
// session's history. The current session isn't deleted (still
// listable via /sessions).
//
// Target is addressed by short-id (first N chars of the UUID);
// numbers from the last /sessions listing are display-only.
func (r *Router) cmdResume(ctx context.Context, from, arg string) (string, error) {
	arg = trimQuotes(strings.TrimSpace(arg))
	if arg == "" {
		return "usage: /resume <shortid>  (use /sessions to find the short-id)", nil
	}
	target, err := r.findSessionForSender(ctx, from, arg)
	if err != nil {
		return err.Error(), nil //nolint:nilerr // legible chat text; the actual error is user-facing
	}
	if err := r.jidMap.Put(ctx, from, target.ID); err != nil {
		return "", fmt.Errorf("rebind jid: %w", err)
	}
	r.logger.Info("router.session_resumed",
		slog.String("from", from),
		slog.String("session_id", target.ID))
	title := target.Title
	if title == "" {
		title = "(untitled)"
	}
	return fmt.Sprintf("resumed session %q (%d msg). next message continues that thread.",
		title, target.MessageCount), nil
}

// cmdSave takes an atomic snapshot of the current session and
// stores it as a separate named session. The current session
// keeps its ID + continues to receive future messages; the
// snapshot is a frozen point-in-time copy the user can later
// /resume to revisit that state without losing the current
// thread. Git-branch analog: work freely + save known-good
// checkpoints.
//
// Distinct from /name (which relabels the current session
// in-place) and /clear (which retires the current session
// and starts an empty one). /save is the "I want to be able
// to come back to THIS state" verb.
func (r *Router) cmdSave(ctx context.Context, from, name string) (string, error) {
	name = trimQuotes(strings.TrimSpace(name))
	if name == "" {
		// No name → auto-generate a timestamped label. Users who
		// type just /save clearly want to save NOW without
		// stopping to think of a name; forcing them to retry
		// with a name is bad UX. They can /name "…" later to
		// give it a memorable label.
		name = "snapshot " + time.Now().UTC().Format("2006-01-02 15:04")
	}
	// Load the current session to snapshot its messages.
	sess, err := r.sessionFor(ctx, from)
	if err != nil {
		return "", fmt.Errorf("session for: %w", err)
	}
	// Build a fresh session with the same messages. Deep-copy
	// the Messages slice so future appends to the live session
	// never mutate the snapshot's history.
	snapshot := agent.NewSession(name)
	snapshot.Sender = from
	if len(sess.Messages) > 0 {
		snapshot.Messages = make([]agent.Message, len(sess.Messages))
		copy(snapshot.Messages, sess.Messages)
	}
	if err := r.store.Save(ctx, snapshot); err != nil {
		return "", fmt.Errorf("save snapshot: %w", err)
	}
	// jidMap intentionally NOT touched — the user stays in the
	// original session and continues there. The snapshot is a
	// separate row addressable via /sessions.
	r.logger.Info("router.session_snapshotted",
		slog.String("from", from),
		slog.String("source_session_id", sess.ID),
		slog.String("snapshot_session_id", snapshot.ID),
		slog.String("name", name),
	)
	return fmt.Sprintf("saved snapshot %q (%d msg). you're still in the current session — /r %s to revisit the snapshot later.",
		name, len(snapshot.Messages), shortSessionID(snapshot.ID)), nil
}

// cmdDelete removes a session by short-id. Refuses to delete
// the current session — the user should /clear first (which
// keeps the old session for audit AND repoints the jidMap to a
// fresh one). Prevents the "I just deleted the conversation I
// was in the middle of" foot-gun.
func (r *Router) cmdDelete(ctx context.Context, from, arg string) (string, error) {
	arg = trimQuotes(strings.TrimSpace(arg))
	if arg == "" {
		return "usage: /delete <shortid>  (use /sessions to find the short-id; /clear if you want to reset the current session)", nil
	}
	target, err := r.findSessionForSender(ctx, from, arg)
	if err != nil {
		return err.Error(), nil //nolint:nilerr // legible chat text
	}
	current, _, _ := r.jidMap.Get(ctx, from) //nolint:errcheck // unknown-current is fine
	if current == target.ID {
		return "refusing to delete the current session — /clear first to move to a fresh one, then /delete this short-id.", nil
	}
	if err := r.store.Delete(ctx, target.ID); err != nil {
		return "", fmt.Errorf("delete session: %w", err)
	}
	r.logger.Info("router.session_deleted",
		slog.String("from", from),
		slog.String("session_id", target.ID))
	title := target.Title
	if title == "" {
		title = "(untitled)"
	}
	// Same scope callout as /clear — the WhatsApp thread is
	// not the bot's DB, and the bot doesn't call WhatsApp's
	// message-delete API. Users chasing "remove the bubbles"
	// have to delete on the phone side themselves.
	return fmt.Sprintf("deleted session %q from my memory.\n(your WhatsApp chat bubbles stay — I can't reach your phone's chat history.)", title), nil
}

// findSessionForSender resolves a short-id (or full ID) to a
// session summary owned by the sender. Refuses to return
// sessions owned by anyone else — the scope-isolation invariant
// /clear guarantees. Ambiguous short-id (matches >1 session)
// returns a legible chat error; caller wraps as reply text.
func (r *Router) findSessionForSender(ctx context.Context, from, needle string) (state.Summary, error) {
	summaries, err := r.store.ListBySender(ctx, from, 0) // 0 = uncapped
	if err != nil {
		return state.Summary{}, fmt.Errorf("list sessions: %w", err)
	}
	var matches []state.Summary
	for _, s := range summaries {
		if strings.HasPrefix(s.ID, needle) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return state.Summary{}, fmt.Errorf("no session found for short-id %q (try /sessions to see the list)", needle)
	case 1:
		return matches[0], nil
	default:
		return state.Summary{}, fmt.Errorf("short-id %q is ambiguous (%d matches) — use more characters", needle, len(matches))
	}
}

// shortSessionID returns the first 8 chars of a UUID for
// human-facing addressing. 8 chars is enough to disambiguate
// hundreds of sessions per sender without being unwieldy on a
// phone screen.
func shortSessionID(id string) string {
	const short = 8
	if len(id) <= short {
		return id
	}
	return id[:short]
}

// trimQuotes strips a single pair of surrounding double quotes
// from a string. Users typing /name "foo bar" on WhatsApp send
// the literal quotes; without stripping the session title would
// include them.
// trimQuotes strips a single surrounding quote pair from s.
// Handles the four quote shapes WhatsApp / iMessage / iOS
// autocorrect actually produce in the wild:
//
//   - ASCII double quotes "…" (what users type on desktop)
//   - Smart double quotes “…” (iOS autocorrect result)
//   - ASCII single quotes '…' (Android alternate)
//   - Smart single quotes ‘…’ (iOS autocorrect result)
//
// Prior behaviour only stripped ASCII double quotes, so a name
// typed on an iPhone landed with the curly quotes intact and
// looked like "“chat”" in /sessions listings.
func trimQuotes(s string) string {
	pairs := []struct{ open, close string }{
		{`"`, `"`}, // ASCII double
		{"“", "”"}, // “ ”
		{`'`, `'`}, // ASCII single
		{"‘", "’"}, // ‘ ’
	}
	for _, p := range pairs {
		if strings.HasPrefix(s, p.open) && strings.HasSuffix(s, p.close) && len(s) >= len(p.open)+len(p.close) {
			return s[len(p.open) : len(s)-len(p.close)]
		}
	}
	return s
}

// cmdHelp returns the operator-facing command listing. Kept as
// a plain function (not a method) because it depends on nothing
// on Router — a static reference card the user can pull up
// with /help or /h from any chat state.
//
// Groups verbs by operator intent (session > turn > identity >
// ops). Long-form + short-form on the same line so the reader
// only scans once. Deliberately does NOT enumerate every alias
// (e.g. /ls, /rm) — noise adds up on a phone screen; the
// canonical + one-char forms are enough for discovery.
//
// The /resume dual-use note is deliberately explicit: same
// verb name shows up in both the session group AND the turn-
// control group. Without the callout users type /resume alone
// expecting session-switch, get "nothing running", and are
// confused.
func cmdHelp() string {
	// WhatsApp bubbles render in a proportional font — the
	// previous column-aligned layout collapsed multi-space
	// gaps and became unreadable. Use WhatsApp-native markup
	// (single-asterisk bold, • bullets, blank lines between
	// sections) so each verb reads as its own line regardless
	// of client font width.
	//
	// Slack + iMessage + Signal also render bold via
	// *asterisks*, so this stays legible across every transport
	// the daemon speaks.
	return `*rousseau commands* — every verb has a shortcut

*session* (my memory of the conversation — not your WhatsApp chat bubbles)
• /save "…" (/s) — atomic snapshot of the current state (stay in current)
• /sessions (/ls) — list your saved sessions
• /name "…" (/n) — rename the current session
• /resume <shortid> (/r) — switch to a saved session
• /clear (/c) — start a fresh session (bot forgets, chat bubbles stay)
• /delete <shortid> (/d) — remove a session (not the current one; bubbles stay)

*turn control* — while a reply is in flight
• /status (/st) — what is the current turn doing?
• /pause (/p) — pause at the next safe checkpoint
• /resume (/r) — unpause a paused turn (or /r <shortid> to switch sessions)
• /cancel (/x) — abort the current turn

*identity + sso*
• /whoami (/w) — show my identity + linked handles
• /link <transport>:<sender> (/lk) — link a handle
• /unlink <transport>:<sender> (/ul) — remove a handle
• /login (/li) — begin an SSO handshake (when enabled)
• /logout (/lo) — end the SSO session

*approvals*
• /approve <token> (/ap) — approve a pending multi-party request
• /deny <token> (/ny) — deny a pending multi-party request

*ops*
• /version (/v) — show the daemon build stamp
• /help (/h) — this listing`
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

// handleSSOCommand handles /login <token> and /logout. Runs before
// the allowlist check so a fresh sender can authenticate their way
// in — the operator hasn't necessarily pre-listed them.
//
// Trust boundary: /login accepts a token, VerifyToken must reject
// anything invalid. The single caller-visible failure surface is
// the reply string; nothing else lands in state unless the token
// verified.
func (r *Router) handleSSOCommand(ctx context.Context, msg IncomingMessage) (string, bool) {
	body := strings.TrimSpace(msg.Body)
	if !strings.HasPrefix(body, "/") {
		return "", false
	}
	parts := strings.Fields(body)
	switch parts[0] {
	case "/login":
		if len(parts) != 2 {
			return "usage: /login <bearer-token>", true
		}
		return r.cmdLogin(ctx, msg.From, parts[1]), true
	case "/logout":
		return r.cmdLogout(ctx, msg.From), true
	}
	return "", false
}

func (r *Router) cmdLogin(ctx context.Context, from, token string) string {
	id, err := r.ssoDir.VerifyToken(ctx, token)
	if err != nil {
		// Fail-closed: never reveal WHY the token failed to the
		// sender — a stranger fuzzing /login must not learn
		// whether they hit an expired-token vs bad-signature
		// branch. Operator sees the reason in the log.
		r.logger.Warn("transport.sso_login_failed",
			slog.String("transport", r.transport),
			slog.String("from", from),
			slog.String("err", err.Error()),
		)
		// Audit emit includes the failure category the SIEM can
		// alert on (repeated denials from one sender = probable
		// attack). The err.Error() is included in Detail because
		// audit records are for the operator, not the sender —
		// the fail-closed reveal only applies to the reply string.
		r.emitAuthAudit(ctx, "login", "", from, "denied", map[string]any{
			"reason": err.Error(),
		})
		return "login: rejected"
	}
	// Determine binding expiry: min(configured TTL, token's exp).
	// The TTL bound guards against a mis-issued long-lived token
	// unlocking a chat identity for its full lifetime.
	exp := id.ExpiresAt
	if exp.IsZero() {
		exp = time.Now().Add(24 * time.Hour) // sane default when token has no exp
	}
	if r.ssoTTL > 0 {
		bounded := time.Now().Add(r.ssoTTL)
		if bounded.Before(exp) {
			exp = bounded
		}
	}
	if err := r.ssoStore.Bind(ctx, r.transport, from, id, exp); err != nil {
		r.logger.Error("transport.sso_bind_failed",
			slog.String("transport", r.transport),
			slog.String("from", from),
			slog.String("subject", id.Subject),
			slog.String("err", err.Error()),
		)
		r.emitAuthAudit(ctx, "login", id.Subject, from, "error", map[string]any{
			"reason": "binding store error: " + err.Error(),
		})
		return "login: internal error"
	}
	r.logger.Info("transport.sso_login",
		slog.String("transport", r.transport),
		slog.String("from", from),
		slog.String("subject", id.Subject),
		slog.Time("expires_at", exp.UTC()),
	)
	r.emitAuthAudit(ctx, "login", id.Subject, from, "success", map[string]any{
		"expires_at": exp.UTC().Format(time.RFC3339),
	})
	name := id.DisplayName
	if name == "" {
		name = id.Subject
	}
	return "signed in as " + name
}

// handleApprovalCommand handles /approve and /deny multi-party
// votes. Returns (reply, true) when the message matched; the
// caller then short-circuits the LLM path.
//
// The voter's SSO identity is drawn from ctx (populated
// upstream by the SSO-store lookup); anonymous voters are
// refused. The PendingManager owns all the vote-counting +
// distinct-approver enforcement — the router just adapts
// chat-command args to method calls.
func (r *Router) handleApprovalCommand(ctx context.Context, msg IncomingMessage) (string, bool) {
	body := strings.TrimSpace(msg.Body)
	if !strings.HasPrefix(body, "/") {
		return "", false
	}
	parts := strings.Fields(body)
	switch parts[0] {
	case "/approve", "/deny":
		if len(parts) != 2 {
			return "usage: " + parts[0] + " <token>", true
		}
		verdict := approval.VerdictApprove
		if parts[0] == "/deny" {
			verdict = approval.VerdictDeny
		}
		var voter string
		if id, ok := sso.IdentityFromContext(ctx); ok {
			voter = id.Subject
		}
		res := r.approvals.Vote(ctx, parts[1], voter, verdict)
		return res.String(), true
	}
	return "", false
}

func (r *Router) cmdLogout(ctx context.Context, from string) string {
	// Look up the identity BEFORE we unbind so the audit record
	// names WHO signed out. Ignore lookup errors — logout is
	// best-effort by design (idempotent).
	var actor string
	if id, ok, err := r.ssoStore.Lookup(ctx, r.transport, from); err == nil && ok {
		actor = id.Subject
	}
	if err := r.ssoStore.Unbind(ctx, r.transport, from); err != nil {
		r.logger.Warn("transport.sso_unbind_failed",
			slog.String("transport", r.transport),
			slog.String("from", from),
			slog.String("err", err.Error()),
		)
		r.emitAuthAudit(ctx, "logout", actor, from, "error", map[string]any{
			"reason": err.Error(),
		})
		return "logout: internal error"
	}
	r.emitAuthAudit(ctx, "logout", actor, from, "success", nil)
	return "signed out"
}

func (r *Router) allowed(ctx context.Context, from string) bool {
	if r.openAll {
		return true
	}
	if _, ok := r.allow[from]; ok {
		return true
	}
	// SSO-verified senders bypass the static allowlist. This is
	// the whole point of wiring SSO — an org with 500 users on
	// Okta shouldn't have to enumerate 500 phone numbers in
	// config.yaml.
	//
	// Lookup filters expired bindings; on any store-level error
	// we deny (fail-CLOSED — an SSO backend hiccup must not open
	// the door to unauthenticated senders).
	if r.ssoStore != nil {
		id, ok, err := r.ssoStore.Lookup(ctx, r.transport, from)
		if err != nil {
			r.logger.Warn("transport.sso_lookup_failed",
				slog.String("transport", r.transport),
				slog.String("from", from),
				slog.String("err", err.Error()),
			)
			return false
		}
		if ok {
			r.logger.Debug("transport.sso_allowed",
				slog.String("transport", r.transport),
				slog.String("from", from),
				slog.String("subject", id.Subject),
			)
			return true
		}
	}
	return false
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
	sess.Sender = jid // enables /sessions to list this session for the sender later
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
