// Package tenant scaffolds multi-tenant support. Rousseau today
// assumes a single-tenant deployment (one identity, one shared
// state, one allowlist). This package types the extensions needed
// to run one daemon serving multiple isolated teams.
//
// # Status
//
// Scaffold only. Types + resolver interface are in place; the
// state-table migrations, the transport-router middleware, and the
// per-tenant approver / vault land as W4.3 in the roadmap.
//
// Every state table gains a `tenant_id` column; every query filters
// on it. Middleware in the transport router extracts the tenant
// from the inbound JID (a config-driven pattern) and stashes it in
// the request context.
package tenant

import (
	"context"
	"errors"
)

// ID is the stable identifier for a tenant. Small (≤ 32 char)
// human-readable strings recommended so operators can grep for
// them in logs.
type ID string

// Config describes one tenant's isolated slice of the daemon.
type Config struct {
	ID ID
	// Allowlist restricts which sender identifiers may talk to this
	// tenant. Empty means "same as the daemon-level allowlist" —
	// useful for a default-tenant fallback.
	Allowlist []string
	// Credentials are the per-tenant secrets (LLM API keys, transport
	// tokens, integration credentials). Kept separate from the daemon
	// config so operators can rotate one tenant without redeploying
	// others.
	Credentials map[string]string
	// ApproverRules optionally overrides the default approver policy
	// for this tenant. Empty inherits.
	ApproverRules []string
}

// Resolver maps an inbound identity (transport + sender JID) to the
// tenant that owns it. Implementations must be safe for concurrent
// use.
type Resolver interface {
	// Resolve returns the tenant ID for the given transport +
	// sender. Empty return value + nil error means "use the
	// daemon-level default tenant."
	Resolve(ctx context.Context, transport, sender string) (ID, error)
}

// key is a private ctx-key type.
type key int

const tenantIDKey key = 0

// WithID returns ctx carrying tenant. Middleware calls this after
// [Resolver.Resolve] so downstream state calls can filter by tenant.
func WithID(ctx context.Context, id ID) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantIDKey, id)
}

// FromContext returns the tenant ID set by [WithID], or the empty
// ID when none is present.
func FromContext(ctx context.Context) ID {
	id, _ := ctx.Value(tenantIDKey).(ID)
	return id
}

// ErrScaffold is returned by every constructor while the runtime is
// being built.
var ErrScaffold = errors.New("tenant: runtime not yet implemented (see docs/multi-tenant.md)")

// NewMapResolver is the intended file-config-driven resolver.
// Scaffold — returns ErrScaffold until W4.3 lands.
func NewMapResolver(_ []Config) (Resolver, error) {
	return nil, ErrScaffold
}
