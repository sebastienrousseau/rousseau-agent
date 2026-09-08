# A2A conformance status

**Status:** partial conformance to A2A v1.0.1 (2026-05-28).
Wire-level v1.0 routes ship alongside the pre-v1 v0-shorthand
routes so peers on either version can talk to rousseau. Follow
the [Roadmap](#roadmap) below for the sequenced path to full
v1.0 conformance.

**Report date:** 2026-09-06 · **Spec version referenced:**
[A2A v1.0.1 (`specification/a2a.proto`)](https://github.com/a2aproject/A2A/blob/main/specification/a2a.proto).

## Why this document exists

rousseau's `internal/a2a` package predates the Linux
Foundation-donated v1.0 spec. Google shipped the original
protocol in April 2025, the LF took stewardship in June 2025,
v0.3.0 landed in July 2025, and v1.0.0/v1.0.1 arrived in the
first half of 2026. Every one of those releases contained
breaking changes.

The package's original code (`v0.0.2` era) implements a
plausible v0-shape of the spec but is not on the wire what a
v1.0-conformant peer will send or expect. This document is the
honest diff between what we ship and what the spec requires, so
operators can plan for what "A2A-compatible" means when they
deploy rousseau today.

## Wire-level diff

Legacy = the routes/shapes present since v0.0.2. Spec = A2A
v1.0.1. Both are served today; the roadmap deprecates legacy
after v0.0.5.

| Concern | Legacy (still served) | Spec v1.0.1 (now served) |
|---|---|---|
| Well-known discovery | `GET /.well-known/agent-capabilities` | `GET /.well-known/agent-card.json` |
| Message submission | `POST /tasks` (task-shaped body) | `POST /message:send` (message-shaped body) |
| Streaming submission | (not distinguished) | `POST /message:stream` (SSE) |
| Cancel | `POST /tasks/{id}/cancel` | `POST /tasks/{id}:cancel` (colon-verb) |
| Re-subscribe | `GET /tasks/{id}/events` | `GET /tasks/{id}:subscribe` |
| Get task | `GET /tasks/{id}` | `GET /tasks/{id}` |
| Response Content-Type | `application/json` | `application/a2a+json` |
| Response header | (none) | `A2A-Version: 1.0` |
| TaskState enum values | `"running"`, `"completed"`, `"failed"`, `"cancelled"` | `TASK_STATE_WORKING`, `TASK_STATE_COMPLETED`, `TASK_STATE_FAILED`, `TASK_STATE_CANCELED` (American spelling per ADR-001) |
| Missing lifecycle states | — | `TASK_STATE_SUBMITTED`, `TASK_STATE_INPUT_REQUIRED`, `TASK_STATE_AUTH_REQUIRED`, `TASK_STATE_REJECTED` |
| Message model | `Task{Prompt string, InputArtifacts[]}` | `Message{messageId, contextId, taskId, role, parts[], extensions[]}` with polymorphic `Part` |
| Artifact model | `{URI, MimeType, Name, SizeBytes}` | `Artifact{artifactId, name, description, parts[], metadata, extensions}` |
| Card shape | flat `CapabilityCard` | rich `AgentCard{capabilities, interfaces[], securitySchemes, security[], signatures[], defaultInput/OutputModes[], provider, iconUrl, documentationUrl}` |
| Errors | `{error: "..."}` | JSON-RPC 2.0 error object OR ProtoJSON error body with typed `details[]` |
| Auth | bearer allowlist | must honor whatever `AgentCard.securitySchemes` declares (five schemes: APIKey / HTTP / OAuth2 / OIDC / mTLS) |
| Push notifications | absent | full CRUD under `/tasks/{id}/pushNotificationConfigs` |
| Transports | HTTP+JSON only | HTTP+JSON REST (shipping) / JSON-RPC 2.0 (planned) / gRPC (not planned) |

## What ships in v0.0.4

- **v1.0 well-known endpoint.** `GET /.well-known/agent-card.json`
  returns a spec-shaped `AgentCard` derived from the operator's
  configured `CapabilityCard`. Legacy `.well-known/agent-capabilities`
  still serves the v0-shorthand card for older peers.
- **v1.0 submission endpoint.** `POST /message:send` accepts a
  spec-shaped `Message` and returns a spec-shaped `Task`.
  Legacy `POST /tasks` still accepts the v0 shape.
- **v1.0 colon-verb routes.** `POST /tasks/{id}:cancel` and
  `GET /tasks/{id}:subscribe` are served alongside the legacy
  slash-suffix equivalents.
- **Spec Content-Type.** New routes respond with
  `application/a2a+json` and set `A2A-Version: 1.0`.
- **v1.0 TaskState enum.** `TASK_STATE_*` string values on the
  wire; internal helpers convert between the v0 legacy strings
  and the spec strings so shared handlers work either way.
- **v1.0 client methods.** `client.SendMessage`,
  `client.SubscribeToTask`, `client.GetAgentCard` hit the new
  spec paths first and fall back to the legacy paths on 404,
  so a rousseau daemon can call peers on either version.

## What doesn't ship yet

Documented gaps against v1.0.1. Filed as follow-ups; none block
interop with peers that use the shipped subset.

1. **JSON-RPC 2.0 binding.** The spec permits three transports;
   we ship REST first because it's what the current codebase and
   deployment story matches. JSON-RPC binding is scheduled for
   v0.0.5.
2. **gRPC binding.** Deliberately deferred — enterprises rarely
   require gRPC A2A today and it would break rousseau's "static
   binary, no cgo, no runtime deps" identity.
3. **Push-notification config CRUD.** `AgentCapabilities.pushNotifications`
   is advertised as `false`; the `/tasks/{id}/pushNotificationConfigs`
   surface is not implemented.
4. ~~**Signed AgentCards.** `AgentCard.signatures[]` is left empty
   today. Card signing (JWS) is on the enterprise-edition
   roadmap alongside SSO/OIDC.~~ **DELIVERED (v0.0.5)**: JWS
   Compact-form signing with Ed25519 (`alg=EdDSA`) via
   `a2a.SignAgentCard` / `a2a.VerifyAgentCard`. Server signs
   when `Server.SigningKey` is set; client verifies against
   `Config.TrustedPublisherKeys`. `Config.RequireSignedCard`
   flips the client into strict mode where unsigned cards are
   also rejected.
5. **Security schemes beyond bearer.** Only bearer-allowlist auth
   ships today; `AgentCard.securitySchemes` advertises exactly
   what we accept. OIDC, mTLS, OAuth2 device-code + PKCE land
   with the enterprise SSO surface.
6. **Extended AgentCard.** `GET /agentCard:extended` and the
   `capabilities.extendedAgentCard` flag are absent — the base
   card is the full card for now.
7. **Per-skill security.** Skills inherit the agent-level auth;
   per-skill `security[]` requirements (v0.3+) are ignored.
8. **`ListTasks` filters.** `GET /tasks` list-with-filters is
   not implemented; the single-task `GET /tasks/{id}` is.
9. **Artifacts as first-class `Part[]`.** Legacy `Artifact{URI, MimeType}`
   is what the current codebase understands. v1.0 spec-shaped
   artifacts are accepted on `/message:send` but internally
   flattened to the legacy shape until we teach the handler
   pipeline to consume `Part` directly.

## Path-agnostic hardening also in v0.0.4

Applies to both legacy and spec routes:

- **Cross-origin redirect refusal** on artifact fetches — was
  already present. Still in place, still correct.
- **Size cap** on `message.parts[]` at the top level, not only on
  individual artifacts, closing a slowloris-shaped gap noted in
  the spec review.
- **`A2A-Extensions` header** — parsed on inbound requests;
  unknown extensions with `X-A2A-Extension-Required: true` cause
  a `ExtensionSupportRequiredError` response.

## Roadmap

Sequenced list, targeting one version bump per row (rousseau
increments by 0.0.1 per release).

| Version | Ship |
|---|---|
| v0.0.4 | **This document.** v1.0 well-known + colon-verb routes + `application/a2a+json` + TaskState v1.0 enum + `SendMessage`/`SubscribeToTask`/`GetAgentCard` client + `examples/embed-a2a-federated`. |
| v0.0.5 | JSON-RPC 2.0 binding as a second transport. ~~AgentCard `signatures[]` (JWS, verify-only)~~ — **shipped**: sign + verify both directions, gated by operator config. Push-notification config CRUD (bare implementation, enterprise-edition webhook egress guarded by license). |
| v0.0.6 | Deprecate legacy routes: log a warning on every hit, plumb `Deprecation:` header. Add `ListTasks` filter surface. |
| v0.0.7 | Remove legacy routes. `Part[]`-native handler pipeline (drop the flatten-to-legacy step). |
| v0.0.8 | Enterprise-edition-only: OIDC + mTLS + OAuth2 device-code auth schemes. Extended AgentCard. Per-skill security. |

## Conformance testing

There is no upstream conformance suite. The A2A project's
canonical repo (`a2aproject/A2A`) has no `/conformance` or
`/tck` directory. Two substitutes ship in this repo:

- **Contract test** (`.github/workflows/a2a-compat.yml`). A CI
  job that starts our v1.0 server, hits every spec route
  through the v1.0 client, and asserts the shape of every
  response — TaskState enum values, well-known path,
  colon-verb routing, `Content-Type`, `A2A-Version` header.
  Regressions surface as a failed CI check.
- **Cross-implementation smoke test** (planned, v0.0.5). Spin
  up the reference Python SDK's `a2a` CLI against our Go
  server in CI and vice-versa. Minimum surface: `a2a discover`,
  `a2a send`, `a2a subscribe`.

## Honest unverified-claim flags

- The Linux Foundation Agentic AI Foundation has not (as of
  2026-09-06) published a formal "A2A-compatible" trademark
  program. Nothing prevents rousseau from claiming
  A2A-compatibility today, but this may change once the LF
  formalizes certification. Track:
  <https://www.linuxfoundation.org/press/a2a-protocol-surpasses-150-organizations-lands-in-major-cloud-platforms-and-sees-enterprise-production-use-in-first-year>.
- `specification/json/` on the A2A repo is scaffolded but
  empty today. If/when JSON Schema drops there, wire it into
  the conformance CI as a third gate.
- The spec permits gRPC-only agents; the shipped subset here
  targets HTTP+REST because a) it's what rousseau serves, and
  b) card discovery over HTTP effectively forces HTTP support
  even for gRPC-primary agents.

## Related

- [`a2a.md`](./a2a.md) — package overview and configuration
  surface.
- [`BUYER.md`](./BUYER.md) — who this conformance work serves
  in the enterprise procurement conversation.
- [`GAP_ANALYSIS_2026.md`](./GAP_ANALYSIS_2026.md) — the
  larger competitive-gap document A2A conformance is one
  workstream of.
- [`../internal/a2a/`](../internal/a2a) — the package.
- [`../examples/embed-a2a-federated/`](../examples/embed-a2a-federated) — self-contained
  demo of the v1.0 wire between two rousseau peers.
