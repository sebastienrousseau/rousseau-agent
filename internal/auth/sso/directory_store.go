package sso

import "context"

// DirectoryStore is the read-side interface an [OIDCDirectory]
// consults to satisfy [Directory.ResolveTransportID]. Ships as
// its own type (not [scim.Store] directly) so this package
// stays independent of any specific directory source — future
// backends (LDAP pull, Azure AD Graph, static YAML) plug in
// against the same shape.
//
// The daemon composes an adapter from its chosen backend to
// this interface at wire-up time; see internal/cli/sso_wire.go.
//
// Implementations MUST be safe for concurrent use — the OIDC
// verifier consults the store from every inbound request that
// carries a resolvable transport identifier.
type DirectoryStore interface {
	// ResolveExternalID returns the identity mapped to
	// externalID. Returns ([Identity]{}, [ErrNotFound]) when
	// no user matches. Callers surface ErrNotFound to
	// distinguish "no directory match" from "backend error";
	// governance layers (RBAC / OPA) treat both the same
	// (deny), but audit records differ.
	ResolveExternalID(ctx context.Context, externalID string) (Identity, error)
}

// WithStore attaches d for use in [Directory.ResolveTransportID].
// Returns o unchanged when d is nil so callers can chain
// unconditionally.
//
// Composition intent: an operator running SCIM-only (no OIDC
// verifier) still gets a Directory that answers
// ResolveTransportID by pointing at a Nop verifier + a real
// DirectoryStore. An operator running OIDC-only (no SCIM) gets
// VerifyToken via JWKS but ResolveTransportID returns
// ErrNotFound. An operator running both gets everything.
func (o *OIDCDirectory) WithStore(d DirectoryStore) *OIDCDirectory {
	if o == nil || d == nil {
		return o
	}
	o.store = d
	return o
}
