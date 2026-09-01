# Workspaces

**Status:** resolver runtime shipped in `v0.0.2` (as `internal/tenant`); renamed to `internal/workspace` and reframed for the open-core model in `v0.0.3`. See [`docs/COMMERCIAL.md`](COMMERCIAL.md) for why SaaS multi-tenancy was ruled out.

[`internal/workspace`](../internal/workspace) offers `NewMapResolver([]Config)` → `Registry` with three allowlist match shapes (exact `<transport>:<sender>`, transport-agnostic `<sender>`, catch-all `*`), plus `ConfigFor(id)` / `All()` accessors for downstream per-workspace credentials + approver rules. Optional per-table `workspace_id` filtering is a follow-up that layers on top of this surface where individual features want it.

## What a workspace is (and is not)

A **workspace** is a routing + config scope inside a single self-hosted rousseau-agent deployment. Think "team", "squad", or "project" — the label an operator uses to say "messages from this Slack channel belong to the platform-eng workspace; ones from that WhatsApp number belong to the founders workspace."

- A workspace **is not** a customer boundary.
- A workspace **is not** a security or confidentiality boundary. An operator with shell access to the daemon can see everything.
- A workspace **is not** a licensing unit. The [licence gate](COMMERCIAL.md) covers the whole daemon.
- Rousseau does not sell "multi-tenant SaaS with per-tenant isolation guarantees." That model is deliberately rejected. Customers who need cross-team isolation run separate daemons.

## Why workspaces exist

Even a single-customer self-hosted deployment often benefits from per-team scoping:

- Route inbound identity → the right approver rules and integration credentials without touching the daemon defaults.
- Present per-team labels in logs and audit trails so an operator can grep "everything platform-eng did today".
- Rotate the founders' Anthropic key without touching platform-eng's config.
- Apply team-specific system-prompt overrides or allow-tools policies.

## Design

### Workspace ID resolution

Every inbound message is tagged with a Workspace ID by transport middleware:

```
inbound message ─┐
                 ▼
       workspace.Resolver.Resolve(transport, sender)
                 │
                 ▼
       workspace.WithID(ctx, id)
                 │
                 ▼
       router → agent → per-workspace defaults resolved from Registry
```

`Resolver` is pluggable. The shipped implementation (`workspace.NewMapResolver`) matches a config-supplied allowlist per workspace.

### Config shape

```yaml
workspaces:
  - id: platform-eng
    allowlist:
      - "+14155551212@s.whatsapp.net"
      - "U01234ABCD"                     # slack user
    credentials:
      anthropic_api_key: ${PLATFORM_ENG_ANTHROPIC_KEY}
    approver_rules:
      - "deny:bash: rm -rf .*"
  - id: founders
    allowlist:
      - "+14155559999@s.whatsapp.net"
    credentials:
      anthropic_api_key: ${FOUNDERS_ANTHROPIC_KEY}
```

Unlisted senders route to the empty-string default workspace (or are rejected when `workspaces.strict: true`).

### Per-workspace approver + hooks

Approver rules and lifecycle hooks resolve per workspace — a `pre_tool_use` hook can be platform-eng-only:

```yaml
workspaces:
  - id: platform-eng
    hooks:
      pre_tool_use:
        - name: alpha-no-secrets
          command: /etc/rousseau/hooks/alpha-scan.sh
```

### Optional storage-layer filtering

Individual state features MAY opt into a `workspace_id` column when per-workspace filtering makes sense (e.g. `session_costs` for per-team accounting). This is deliberately NOT a blanket schema-wide guarantee — the model rejects the SaaS notion of "every table is workspace-isolated at the storage layer." Instead each feature adds the column when it needs to answer a per-workspace question.

Example: cost accounting.

```sql
ALTER TABLE session_costs ADD COLUMN workspace_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_session_costs_workspace ON session_costs(workspace_id);
```

Enables `rousseau session cost --group-by workspace --since 30d`.

## Non-goals

- **No isolation guarantees.** Workspaces do not enforce confidentiality between teams sharing one daemon. A shell on the box reads everything.
- **No SaaS-shaped features.** No per-workspace licensing, no per-workspace billing, no self-service provisioning. A workspace is declared in `config.yaml` by the operator.
- **No cross-workspace messaging boundaries in code.** Where a feature wants per-workspace scoping (audit trails, cost accounting), it filters on `workspace_id` itself.

## Success metric

Two teams share one daemon:

- Platform-eng sends a message from JID X → response uses platform-eng's API key, hits platform-eng's approver rules, logs are tagged `workspace=platform-eng`.
- Founders send a message from JID Y → logs are tagged `workspace=founders`; audit trail can be filtered by workspace.
- `rousseau session list --workspace platform-eng` returns only platform-eng's sessions.
