package transport

import (
	"context"
	"log/slog"
	"strings"
)

// SilenceRejected is the outermost gate: a sender that allowed
// rejects reaches nothing downstream and gets no reply, so control
// verbs, rate-limit notices and command errors never confirm to a
// stranger that the bot exists or what it runs. The one exception is
// /login when SSO is on, since signing in is how a new sender becomes
// allowed.
func SilenceRejected(next Handler, allowed func(context.Context, string) bool, ssoLogin bool, logger *slog.Logger) Handler {
	return HandlerFunc(func(ctx context.Context, msg IncomingMessage) (string, error) {
		if allowed(ctx, msg.From) || ssoLogin && isLogin(msg.Body) {
			return next.Handle(ctx, msg)
		}
		logger.Warn("transport.rejected", slog.String("from", msg.From))
		return "", nil
	})
}

// isLogin reports whether body is the /login command or its shortcut.
func isLogin(body string) bool {
	verb, _, _ := strings.Cut(canonicalCommand(body), " ")
	return strings.TrimSpace(verb) == "/login"
}
