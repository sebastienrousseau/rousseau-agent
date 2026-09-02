package sso

import (
	"context"
	"time"
)

// BindingStore persists verified SSO identities keyed by their
// transport-native identifier. Purpose: the daemon's transports
// (WhatsApp / Slack / Matrix / …) don't carry JWTs on every
// message, so we need a way to remember "the sender behind
// whatsapp:+14155551212 is the SSO identity `okta|abc123`,
// verified 2m ago via a /login command."
//
// This is the client-side cousin of the directory-sync surface
// deferred in the OIDC pilot: the operator's users bind
// themselves by presenting a token, rather than the daemon
// pulling the directory via SCIM. Both patterns will co-exist
// in production; this one is what unblocks day-one adoption
// with zero IdP-side plumbing.
//
// # Concurrency
//
// Implementations MUST be safe for concurrent use — the store is
// read from every inbound message on hot transports.
//
// # Expiry
//
// [Lookup] MUST filter expired bindings and treat them as
// not-found. Callers rely on this to avoid re-checking exp
// themselves. [Bind] takes a wall-clock expiresAt so the store
// can prune / filter deterministically.
type BindingStore interface {
	// Bind records that (transport, externalID) is authenticated
	// as id, valid until expiresAt. Replaces any prior binding
	// for the same key.
	Bind(ctx context.Context, transport, externalID string, id Identity, expiresAt time.Time) error
	// Lookup returns the Identity bound to (transport, externalID).
	// Returns (zero, false, nil) when no binding exists or when
	// the binding has expired.
	Lookup(ctx context.Context, transport, externalID string) (Identity, bool, error)
	// Unbind removes the mapping. Idempotent — removing a
	// non-existent binding is not an error.
	Unbind(ctx context.Context, transport, externalID string) error
	// Count returns the number of active (unexpired) bindings.
	// Used by `rousseau doctor` to surface "how many users are
	// signed in via SSO right now?"
	Count(ctx context.Context) (int, error)
}

// NoBindings is a fail-safe [BindingStore] that persists nothing.
// Every Lookup misses, Bind is a no-op that returns nil, Count
// always returns 0. Used when the daemon is configured without
// SSO — the router's SSO code paths become inert without
// requiring nil-checks at every call site.
type NoBindings struct{}

// Bind satisfies [BindingStore].
func (NoBindings) Bind(context.Context, string, string, Identity, time.Time) error {
	return nil
}

// Lookup satisfies [BindingStore].
func (NoBindings) Lookup(context.Context, string, string) (Identity, bool, error) {
	return Identity{}, false, nil
}

// Unbind satisfies [BindingStore].
func (NoBindings) Unbind(context.Context, string, string) error { return nil }

// Count satisfies [BindingStore].
func (NoBindings) Count(context.Context) (int, error) { return 0, nil }
