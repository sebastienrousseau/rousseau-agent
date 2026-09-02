package sso

import "context"

// identityCtxKey is the private key type context values use so no
// other package can collide with it. Standard Go ctx-key pattern.
type identityCtxKey int

const identityKey identityCtxKey = 0

// WithIdentity returns a new context carrying id. Middleware
// (transport.Router after a successful BindingStore.Lookup) calls
// this so downstream code — approvers, tools, audit sinks — can
// read the verified identity without re-plumbing arguments.
//
// Passing the zero-value Identity returns ctx unchanged (safe to
// call unconditionally). "Subject == ”" is treated as the sentinel
// for "no identity attached" so callers can detect anonymous
// requests without ok-return semantics.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	if id.Subject == "" {
		return ctx
	}
	return context.WithValue(ctx, identityKey, id)
}

// IdentityFromContext returns the Identity attached to ctx by
// [WithIdentity], or the zero value when no identity is present.
// The second return value is true only when an identity was found
// — callers that need to distinguish "anonymous" from "identity
// with an empty display name" should check it.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}
