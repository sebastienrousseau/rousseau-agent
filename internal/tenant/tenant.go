// Package tenant implements multi-tenant support. Rousseau's default
// deployment is single-tenant (one identity, one shared state, one
// allowlist); this package layers the extensions needed to run one
// daemon serving multiple isolated teams.
//
// # Model
//
// Every state table gains a `tenant_id` column; every query filters
// on it. Middleware in the transport router extracts the tenant from
// the inbound identity (via [Resolver.Resolve]) and stashes it in the
// request context with [WithID]. Downstream state calls then read it
// back with [FromContext].
//
// # Status
//
// Runtime shipped in `v0.0.2`:
//
//   - `NewMapResolver(configs)` gives operators a config-driven
//     resolver that matches inbound `(transport, sender)` pairs
//     against per-tenant allowlist entries with three supported
//     patterns: exact `<transport>:<sender>`, transport-agnostic
//     `<sender>`, and catch-all `*`. First-match wins.
//   - `Registry` provides `ConfigFor(id)` for downstream code that
//     needs per-tenant credentials / approver rules.
//
// State-table migrations and per-tenant approver / vault wiring are
// follow-ups that plug on top of the same [Resolver] + [Registry]
// surface.
package tenant

import (
	"context"
	"errors"
	"strings"
)

// ID is the stable identifier for a tenant. Small (≤ 32 char)
// human-readable strings recommended so operators can grep for them
// in logs.
type ID string

// Config describes one tenant's isolated slice of the daemon.
type Config struct {
	ID ID
	// Allowlist entries recognise three shapes:
	//   - "<transport>:<sender>" — exact match (e.g. "whatsapp:+15551234567").
	//   - "<sender>"              — matches on any transport
	//                               (e.g. "+15551234567").
	//   - "*"                     — catch-all; use for a default tenant.
	//
	// First-match wins across the config list.
	Allowlist []string
	// Credentials are per-tenant secrets (LLM API keys, transport
	// tokens, integration credentials). Kept separate from the daemon
	// config so operators can rotate one tenant without redeploying
	// others.
	Credentials map[string]string
	// ApproverRules optionally overrides the default approver policy
	// for this tenant. Empty inherits.
	ApproverRules []string
}

// Resolver maps an inbound identity (transport + sender) to the
// tenant that owns it. Implementations must be safe for concurrent
// use.
type Resolver interface {
	// Resolve returns the tenant ID for the given transport +
	// sender. Empty return value + nil error means "use the
	// daemon-level default tenant."
	Resolve(ctx context.Context, transport, sender string) (ID, error)
}

// Registry exposes tenant Configs for downstream code that needs
// per-tenant credentials, approver rules, etc.
type Registry interface {
	Resolver
	// ConfigFor returns the Config for id, or (Config{}, false).
	ConfigFor(id ID) (Config, bool)
	// All returns every registered tenant Config.
	All() []Config
}

// NewMapResolver builds a [Registry] from a set of tenant configs.
// Returns an error when any two configs share an ID.
func NewMapResolver(configs []Config) (Registry, error) {
	byID := make(map[ID]Config, len(configs))
	for _, c := range configs {
		if c.ID == "" {
			return nil, errors.New("tenant: Config.ID is required")
		}
		if _, dup := byID[c.ID]; dup {
			return nil, errors.New("tenant: duplicate tenant ID " + string(c.ID))
		}
		byID[c.ID] = c
	}
	// Preserve the caller's order for first-match determinism.
	ordered := make([]Config, len(configs))
	copy(ordered, configs)
	return &mapResolver{byID: byID, ordered: ordered}, nil
}

type mapResolver struct {
	byID    map[ID]Config
	ordered []Config
}

func (r *mapResolver) Resolve(_ context.Context, transport, sender string) (ID, error) {
	key := transport + ":" + sender
	for _, c := range r.ordered {
		for _, entry := range c.Allowlist {
			if entry == "*" || entry == key || entry == sender {
				return c.ID, nil
			}
			if strings.HasPrefix(entry, "*:") && strings.TrimPrefix(entry, "*:") == sender {
				return c.ID, nil
			}
		}
	}
	return "", nil
}

func (r *mapResolver) ConfigFor(id ID) (Config, bool) {
	c, ok := r.byID[id]
	return c, ok
}

func (r *mapResolver) All() []Config {
	out := make([]Config, len(r.ordered))
	copy(out, r.ordered)
	return out
}

// key is a private ctx-key type.
type key int

const tenantIDKey key = 0

// WithID returns ctx carrying tenant. Middleware calls this after
// [Resolver.Resolve] so downstream state calls can filter by tenant.
// Passing an empty id returns ctx unchanged (safe to call
// unconditionally).
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
