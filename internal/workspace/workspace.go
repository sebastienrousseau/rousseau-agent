// Package workspace models logical workspaces (teams / squads /
// projects) within a single on-premise rousseau-agent deployment.
//
// # Not multi-tenant SaaS
//
// This package was originally shaped as SaaS multi-tenant
// isolation. That model is deliberately rejected by the
// commercialization strategy (see [`docs/COMMERCIAL.md`]) — the
// product ships as a single static binary that customers self-
// host. A workspace is NOT a customer boundary, NOT a security
// boundary, and NOT a billing unit. It's a routing / config
// scope: "messages from this Slack channel belong to the
// platform-eng workspace; ones from that WhatsApp number belong
// to the founders workspace."
//
// Concretely, workspaces let a single daemon:
//
//   - route inbound identity → the right approver rules / vault
//   - scope per-team defaults (system prompt, allowed tools,
//     integration credentials) without demanding a separate
//     deploy per team
//   - present per-team labels in logs and audit so an operator
//     can grep for "everything the platform-eng workspace did"
//
// It does NOT:
//
//   - enforce isolation between workspaces at the storage layer
//     (tables are shared)
//   - support per-workspace licensing (the licence gates the
//     whole daemon)
//   - deliver on any confidentiality claim between workspaces
//     (an operator with shell access to the daemon can read
//     anything)
//
// # Model
//
// Every state row MAY carry a `workspace_id` column when a
// feature needs per-workspace filtering; today only routing +
// per-workspace credentials use it. Middleware in the transport
// router extracts the workspace from the inbound identity (via
// [Resolver.Resolve]) and stashes it in the request context with
// [WithID]. Downstream code reads it back with [FromContext].
//
// # Status
//
//   - `NewMapResolver(configs)` gives operators a config-driven
//     resolver that matches inbound `(transport, sender)` pairs
//     against per-workspace allowlist entries with three
//     supported patterns: exact `<transport>:<sender>`,
//     transport-agnostic `<sender>`, and catch-all `*`. First-
//     match wins.
//   - `Registry` provides `ConfigFor(id)` for downstream code
//     that needs per-workspace credentials or approver rules.
//
// Per-workspace approver / vault wiring plugs on top of the
// same [Resolver] + [Registry] surface.
//
// [`docs/COMMERCIAL.md`]: ../../docs/COMMERCIAL.md
package workspace

import (
	"context"
	"errors"
	"strings"
)

// ID is the stable identifier for a workspace. Short (≤ 32 char)
// human-readable strings recommended so operators can grep for
// them in logs and audit trails.
type ID string

// Config describes one workspace's routing scope and per-team
// defaults inside the shared daemon.
type Config struct {
	ID ID
	// Allowlist entries recognise three shapes:
	//   - "<transport>:<sender>" — exact match (e.g. "whatsapp:+15551234567").
	//   - "<sender>"              — matches on any transport
	//                               (e.g. "+15551234567").
	//   - "*"                     — catch-all; use for a default workspace.
	//
	// First-match wins across the config list.
	Allowlist []string
	// Credentials are per-workspace secrets (LLM API keys, transport
	// tokens, integration credentials). Kept separate from the daemon
	// config so operators can rotate one workspace without touching
	// others.
	Credentials map[string]string
	// ApproverRules optionally overrides the default approver policy
	// for this workspace. Empty inherits from the daemon default.
	ApproverRules []string
}

// Resolver maps an inbound identity (transport + sender) to the
// workspace that owns it. Implementations must be safe for
// concurrent use.
type Resolver interface {
	// Resolve returns the workspace ID for the given transport +
	// sender. Empty return value + nil error means "no workspace
	// matched — the caller should fall through to the daemon-level
	// default."
	Resolve(ctx context.Context, transport, sender string) (ID, error)
}

// Registry exposes workspace Configs for downstream code that
// needs per-workspace credentials, approver rules, etc.
type Registry interface {
	Resolver
	// ConfigFor returns the Config for id, or (Config{}, false).
	ConfigFor(id ID) (Config, bool)
	// All returns every registered workspace Config.
	All() []Config
}

// NewMapResolver builds a [Registry] from a set of workspace
// configs. Returns an error when any two configs share an ID.
func NewMapResolver(configs []Config) (Registry, error) {
	byID := make(map[ID]Config, len(configs))
	for _, c := range configs {
		if c.ID == "" {
			return nil, errors.New("workspace: Config.ID is required")
		}
		if _, dup := byID[c.ID]; dup {
			return nil, errors.New("workspace: duplicate workspace ID " + string(c.ID))
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

const workspaceIDKey key = 0

// WithID returns ctx carrying id. Middleware calls this after
// [Resolver.Resolve] so downstream code can filter by workspace.
// Passing an empty id returns ctx unchanged (safe to call
// unconditionally).
func WithID(ctx context.Context, id ID) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, workspaceIDKey, id)
}

// FromContext returns the workspace ID set by [WithID], or the
// empty ID when none is present.
func FromContext(ctx context.Context) ID {
	id, _ := ctx.Value(workspaceIDKey).(ID)
	return id
}
