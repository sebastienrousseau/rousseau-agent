package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/approval"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	"github.com/sebastienrousseau/rousseau-agent/internal/identity"
)

// Identity linking, SSO login and approval commands, and the sender allow checks.

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

// Allowed reports whether from may use the agent: on the static
// allowlist, or holding a valid SSO binding. Transports call it before
// costly pre-processing (media download, transcription) so a stranger
// cannot spend bandwidth, CPU or API budget; the full decision still
// happens in Handle.
func (r *Router) Allowed(ctx context.Context, from string) bool { return r.allowed(ctx, from) }

// SSOEnabled reports whether this router answers /login and /logout
// from senders it does not yet allow (SSO is configured).
func (r *Router) SSOEnabled() bool { return r.ssoDir != nil }

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
