package transport

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// Session slash commands: /new, /sessions, /name, /resume, /save, /find, /delete, /help.

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
	case "/link", "/unlink":
		return r.handleHandleCommand(ctx, msg.From, parts), true
	case "/confirm":
		if len(parts) != 2 {
			return "usage: /confirm <code>", true
		}
		return r.cmdConfirm(ctx, msg.From, parts[1]), true
	}
	return "", false
}

// handleHandleCommand runs /link and /unlink <transport>:<sender>.
func (r *Router) handleHandleCommand(ctx context.Context, from string, parts []string) string {
	verb := parts[0]
	if len(parts) != 2 || !strings.Contains(parts[1], ":") {
		return "usage: " + verb + " <transport>:<sender>"
	}
	tp, sender, _ := strings.Cut(parts[1], ":")
	if verb == "/link" {
		return r.cmdLink(ctx, from, tp, sender)
	}
	return r.cmdUnlink(ctx, from, tp, sender)
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
	defer r.senders.Lock(from)() // same lock as the session lookup it rebinds
	sess := agent.NewSession("chat: " + from)
	sess.Sender = r.key(from) // so it surfaces in /sessions later
	if err := r.store.Save(ctx, sess); err != nil {
		return "", fmt.Errorf("save fresh session: %w", err)
	}
	// Overwrites the previous mapping via Put's upsert
	// semantics. Both driver implementations expose this as
	// ON CONFLICT DO UPDATE — no delete step needed.
	if err := r.jidMap.Put(ctx, r.key(from), sess.ID); err != nil {
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
	summaries, err := r.store.ListBySender(ctx, r.key(from), listCap)
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}
	if len(summaries) == 0 {
		return "no saved sessions yet. any message you send here starts one — use /name \"…\" to give it a memorable label.", nil
	}
	current, _, _ := r.jidMap.Get(ctx, r.key(from)) //nolint:errcheck // "unknown" is a valid state → current == ""
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
		return err.Error(), nil // legible chat text; the lookup error is user-facing
	}
	defer r.senders.Lock(from)()
	// Resuming is activity: without touching UpdatedAt, idle rotation
	// would move the sender off a resumed old session on the very
	// next message.
	if r.idleAfter > 0 {
		sess, err := r.store.Load(ctx, target.ID)
		if err != nil {
			return "", fmt.Errorf("load resumed session: %w", err)
		}
		sess.UpdatedAt = r.now().UTC()
		if err := r.store.Save(ctx, sess); err != nil {
			return "", fmt.Errorf("touch resumed session: %w", err)
		}
	}
	if err := r.jidMap.Put(ctx, r.key(from), target.ID); err != nil {
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
	snapshot.Sender = r.key(from)
	if len(sess.Messages) > 0 {
		snapshot.Messages = make([]agent.Message, len(sess.Messages))
		copy(snapshot.Messages, sess.Messages)
	}
	if err := r.store.Save(ctx, snapshot); err != nil {
		return "", fmt.Errorf("save snapshot: %w", err)
	}
	note := ""
	if r.forkSess != nil {
		if err := r.forkSess(ctx, sess.ID, snapshot.ID); err != nil {
			r.logger.Warn("router.snapshot_fork_failed",
				slog.String("from", from), slog.String("err", err.Error()))
			note = "\n(note: saved without the model's history; resuming this snapshot starts a fresh conversation.)"
		}
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
	return fmt.Sprintf("saved snapshot %q (%d msg). you're still in the current session — /r %s to revisit the snapshot later.%s",
		name, len(snapshot.Messages), shortSessionID(snapshot.ID), note), nil
}

// cmdDelete removes a session by short-id. Refuses to delete
// the current session — the user should /clear first (which
// keeps the old session for audit AND repoints the jidMap to a
// fresh one). Prevents the "I just deleted the conversation I
// was in the middle of" foot-gun.
// cmdFind full-text-searches the sender's own sessions and
// returns matching hits with a short snippet. Uses the store's
// SearchBySender (sqlite FTS5 / postgres tsvector — both drivers
// return the same SearchHit shape) so the query hits an index
// rather than scanning payloads.
//
// Scope-isolation: SearchBySender filters on sender at the SQL
// level, so a query from Alice can never surface a hit from
// Bob's sessions regardless of what words match. Pinned by
// TestRouter_FindIsScopedToSender.
func (r *Router) cmdFind(ctx context.Context, from, arg string) (string, error) {
	arg = trimQuotes(strings.TrimSpace(arg))
	if arg == "" {
		return "usage: /find \"words to search for\"  (or /f — matches text in any of your saved sessions)", nil
	}
	hits, err := r.store.SearchBySender(ctx, r.key(from), arg, sqlitestore.SearchOptions{Limit: 10})
	if err != nil {
		return "", fmt.Errorf("search by sender: %w", err)
	}
	if len(hits) == 0 {
		return fmt.Sprintf("no matches for %q in your sessions.", arg), nil
	}
	current, _, _ := r.jidMap.Get(ctx, r.key(from)) //nolint:errcheck // unknown-current is fine

	var b strings.Builder
	fmt.Fprintf(&b, "matches for %q (%d):\n", arg, len(hits))
	for i, h := range hits {
		marker := " "
		if h.SessionID == current {
			marker = "*"
		}
		title := h.Title
		if title == "" {
			title = "(untitled)"
		}
		snippet := strings.TrimSpace(h.Snippet)
		if snippet == "" {
			snippet = "(no preview)"
		}
		fmt.Fprintf(&b, "%s %2d. %s  %s\n    ↳ %s\n",
			marker, i+1, title, shortSessionID(h.SessionID), snippet)
	}
	b.WriteString("\n* = current • /r <shortid> to jump to a match")
	return b.String(), nil
}

func (r *Router) cmdDelete(ctx context.Context, from, arg string) (string, error) {
	arg = trimQuotes(strings.TrimSpace(arg))
	if arg == "" {
		return "usage: /delete <shortid>  (use /sessions to find the short-id; /clear if you want to reset the current session)", nil
	}
	target, err := r.findSessionForSender(ctx, from, arg)
	if err != nil {
		return err.Error(), nil //nolint:nilerr // legible chat text
	}
	current, _, _ := r.jidMap.Get(ctx, r.key(from)) //nolint:errcheck // unknown-current is fine
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
	summaries, err := r.store.ListBySender(ctx, r.key(from), 0) // 0 = uncapped
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
• /find "…" (/f) — full-text search across your sessions
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
• /confirm <code> — confirm a /link from the handle being linked
• /login (/li) — begin an SSO handshake (when enabled)
• /logout (/lo) — end the SSO session

*approvals*
• /pending — list open multi-party requests with what each would run
• /approve <token> <digest> (/ap) — approve a request; quote the digest /pending shows
• /deny <token> (/ny) — deny a pending multi-party request

*ops*
• /version (/v) — show the daemon build stamp
• /help (/h) — this listing`
}
