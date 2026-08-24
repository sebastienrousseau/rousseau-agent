# Agent-to-Agent (A2A) protocol

**Status: scaffold shipped in `v0.0.1`, runtime is a follow-up.**

The [`internal/a2a`](../internal/a2a) package defines the payload
types and interfaces rousseau-agent will use to speak the
[google/A2A](https://google.github.io/A2A/) protocol. The wire-level
HTTP + SSE transport is intentionally deferred: the spec is
substantial enough that a full implementation belongs in its own
release cycle, and by the time we ship it we'd like the ecosystem to
have converged on which optional parts matter.

## Why A2A

Enterprises increasingly deploy several agents that collaborate:
one drafts a spec, another implements, a third reviews. A2A is
emerging as the standard protocol for that handoff. **Being
interoperable expands the "yes, rousseau fits your stack" answer** —
buyers who've committed to another vendor's spec-drafting agent
should still be able to plug rousseau in as the implementer.

Adjacent protocols we're tracking:

- **MCP** (already shipped) — for tool exposure between an agent
  and a host (Claude Code, Cursor, our own client). Different layer.
- **ACP** (OpenHands' ACP) — similar goals, less mindshare.
- **Nostr / Buzz** (Block's) — cryptographic identity for agent
  chains-of-custody. Complementary; would sit on top of A2A.

## Surface (planned)

### Server (rousseau accepts inbound tasks)

```
GET  /.well-known/agent-capabilities     → JSON CapabilityCard
POST /tasks                              → accept a Task, return task_id
GET  /tasks/{id}                         → poll status
GET  /tasks/{id}/events                  → SSE stream of TaskUpdate
POST /tasks/{id}/cancel                  → cancel
```

Bearer-token auth required in production. Configured via:

```yaml
a2a:
  server:
    enabled: true
    listen: :7443
    auth_tokens_file: /etc/rousseau/a2a-tokens
    exposed_skills:                # explicit allow-list — no default exposure
      - review-diff
      - podman-quadlet
```

### Client (rousseau dispatches to peers)

```yaml
a2a:
  clients:
    - name: spec-writer
      endpoint: https://spec-writer.internal/a2a
      auth_header: Bearer ${A2A_SPEC_WRITER_TOKEN}
```

The Handler type in `internal/a2a/server` receives inbound tasks and
runs them as ordinary [`agent.Turn`](../internal/agent/agent.go) calls
on per-task sessions — reusing the existing tool registry, approver,
skills, and hooks pipeline.

## Non-goals

- **No implicit skill exposure.** Every skill an operator wants to
  expose to peers must be listed under `exposed_skills`. Default
  posture is "no capability card entries."
- **No peer discovery service.** Rousseau doesn't run its own
  registry; operators configure peer endpoints statically. This
  keeps the network surface predictable for compliance audits.
- **No untrusted-peer sandbox reuse.** Tasks arriving over A2A are
  handled with the *same* [sandbox backend](./security/sandbox.md)
  the local agent uses. There is no separate escalation for A2A —
  running an A2A server exposes exactly the same surface as running
  a chat transport with an allowlist.

## Follow-up tickets

- HTTP handler wiring (server) + SSE emitter
- HTTP client with retry + backoff (client)
- Auth-token loader with hot-reload on file change
- Rate-limiter middleware per peer
- Capability-card renderer sourced from skills + tool registry
- End-to-end test: two rousseau instances, one submits to the other
