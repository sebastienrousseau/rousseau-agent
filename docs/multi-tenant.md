# Multi-tenant mode (W4.3)

**Status:** [`internal/tenant`](../internal/tenant) package skeleton
shipped in `v0.0.1`; runtime is a follow-up.

## Why

Rousseau today assumes one operator, one allowlist, one shared
state. Enterprise deployments frequently want a single daemon
serving multiple teams — each with independent state, credentials,
and approval policy — without paying the ops cost of running N
containers.

Reference points:

- Devin teamspaces (isolated per-team runs, shared platform)
- Kilo Code cloud tier (per-org gateway, isolated state)

## Design

### Tenant ID resolution

Every inbound message is tagged with a Tenant ID by transport
middleware:

```
inbound message ─┐
                 ▼
       tenant.Resolver.Resolve(transport, sender)
                 │
                 ▼
       tenant.WithID(ctx, id)
                 │
                 ▼
       router → agent → state (every SQL query filters on tenant_id)
```

`Resolver` is pluggable. The first implementation
(`tenant.NewMapResolver`) matches a config-supplied allowlist per
tenant.

### Schema migration

Every state table gains a `tenant_id TEXT NOT NULL DEFAULT ''`
column. Existing rows migrate to `tenant_id = ''` which is the
"default tenant" — behaviourally identical to today's single-
tenant mode.

```sql
ALTER TABLE sessions          ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '';
ALTER TABLE claude_sessions   ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '';
ALTER TABLE session_costs     ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '';
ALTER TABLE cron_jobs         ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '';
-- (…every other state table…)

CREATE INDEX idx_<table>_tenant ON <table>(tenant_id);
```

Every SELECT / UPDATE / DELETE in the state layer is rewritten to
include `WHERE tenant_id = ?` — the ctx-carried tenant ID.

### Config shape

```yaml
tenants:
  - id: team-alpha
    allowlist:
      - "+14155551212@s.whatsapp.net"
      - "U01234ABCD"                     # slack user
    credentials:
      anthropic_api_key: ${TEAM_ALPHA_ANTHROPIC_KEY}
    approver_rules:
      - "deny:bash: rm -rf .*"
  - id: team-beta
    allowlist:
      - "+14155559999@s.whatsapp.net"
    credentials:
      anthropic_api_key: ${TEAM_BETA_ANTHROPIC_KEY}
```

Unlisted senders route to the empty-string default tenant (or are
rejected when `tenants.strict: true`).

### Approver + hooks per tenant

Approver rules and lifecycle hooks resolve per tenant — a
`pre_tool_use` hook can be team-A-only. Config schema:

```yaml
tenants:
  - id: team-alpha
    hooks:
      pre_tool_use:
        - name: alpha-no-secrets
          command: /etc/rousseau/hooks/alpha-scan.sh
```

### Cost accounting per tenant

`rousseau session cost --group-by tenant --since 30d` groups
`session_costs.tenant_id`. Enables per-team billing.

## Non-goals

- **Not a multi-daemon replacement.** Rousseau still runs as one
  process; if a tenant wants complete isolation (separate systemd
  unit, separate container), that's a separate deployment
  decision. Multi-tenant mode is for cases where operational overhead
  matters more than blast-radius isolation.
- **Not a self-service portal.** No REST API for tenants to
  provision themselves; every tenant is operator-declared in
  config.yaml.
- **No cross-tenant messaging.** A tenant cannot see another
  tenant's sessions, costs, or credentials. Enforced by the
  tenant_id filter at every SQL boundary.

## Success metric

Two teams share one daemon:

- Team A sends a message from JID X → response uses Team A's API
  key, hits Team A's approver rules, writes to Team A's session
  store.
- Team B sends a message from JID Y → completely isolated from Team
  A (verifiable by inspecting `session_costs` and `sessions` in
  SQLite).
- `rousseau session list --tenant team-a` returns only Team A's
  sessions.
