// Package toolcontext plumbs per-turn agent state (Session, Provider,
// Logger) into the [tools.Tool] Execute call via context values.
//
// The [tools.Tool] interface intentionally takes only ctx + input so
// that most tools stay stateless. Some tools — spawn_subagent is the
// motivating case — need access to the parent Session and the parent
// Provider to do their work. Rather than widening the Tool interface
// or threading these through every constructor, we inject them into
// ctx at the runTools call site and let interested tools read them.
//
// This package intentionally deals in [any] rather than importing
// concrete types from `internal/agent` — that would create an import
// cycle since `internal/agent` calls WithSession/WithProvider. Callers
// perform the type assertion themselves, which is idiomatic for
// context.Value shapes anyway.
//
// Tools that don't need this state ignore the context values entirely.
package toolcontext

import (
	"context"
	"log/slog"
)

// key is a private context-key type so external packages cannot
// collide with our keys (per Go's stdlib recommendation).
type key int

const (
	sessionKey key = iota
	providerKey
	loggerKey
)

// WithSession returns a derived context carrying session. Tools invoked
// on this context retrieve it via [Session] and cast to their expected
// concrete type (typically *agent.Session).
func WithSession(ctx context.Context, session any) context.Context {
	if session == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionKey, session)
}

// Session returns the value set by [WithSession] as an untyped [any].
// The bool is false when nothing was set. Callers should type-assert:
//
//	raw, ok := toolcontext.Session(ctx)
//	if !ok { … }
//	session, ok := raw.(*agent.Session)
func Session(ctx context.Context) (any, bool) {
	v := ctx.Value(sessionKey)
	return v, v != nil
}

// WithProvider returns a derived context carrying provider. Tools that
// spawn sub-agents against the parent's provider retrieve it via
// [Provider] and cast to their expected concrete type (typically
// agent.Provider).
func WithProvider(ctx context.Context, provider any) context.Context {
	if provider == nil {
		return ctx
	}
	return context.WithValue(ctx, providerKey, provider)
}

// Provider returns the value set by [WithProvider] as an untyped [any].
// The bool is false when nothing was set.
func Provider(ctx context.Context) (any, bool) {
	v := ctx.Value(providerKey)
	return v, v != nil
}

// WithLogger returns a derived context that carries lg for use by
// tools that want to emit structured events on the agent's logger
// rather than the process default.
func WithLogger(ctx context.Context, lg *slog.Logger) context.Context {
	if lg == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey, lg)
}

// Logger returns the [*slog.Logger] set by [WithLogger], or
// [slog.Default] if none is present. Never returns nil.
func Logger(ctx context.Context) *slog.Logger {
	if lg, ok := ctx.Value(loggerKey).(*slog.Logger); ok && lg != nil {
		return lg
	}
	return slog.Default()
}
