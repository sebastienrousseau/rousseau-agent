# Commercial model — rousseau-agent

_Last touched: 2026-08-30._

This doc is the contract between the open-source `rousseau-agent`
core and the paid **Enterprise / Team Edition**. It exists so every
maintainer, contributor, and prospective enterprise buyer can point
at a single page that answers "what does the license unlock, and
what stays free?"

Companion to [`docs/ROADMAP.md`](ROADMAP.md) (what we're building
and when) and [`README.md`](../README.md) (how to use it).

---

## 1. Model

**Open-Core with a paid, self-hosted Enterprise / Team Edition
delivered as an offline license key inside the same static binary.**

- The `rousseau-agent` core (this repository, Apache-2.0 / MIT
  dual licence) is fully functional on its own. Every default
  ships in the core.
- Enterprise / Team features live in the same static binary. They
  are **runtime-gated** by a signed license, not compiled out.
  There is no separate "enterprise binary" — the same `rousseau`
  you download from GitHub Releases is what a paying customer
  runs, with a `ROUSSEAU_LICENSE_KEY` env var (or a mode-0600
  file) set.
- **Zero telemetry, no license server, no phone home.** Verification
  is Ed25519-signature-check against public keys embedded in the
  binary at build time. An airgapped datacentre can activate an
  enterprise licence the same way it would activate any other
  offline product: paste the key.
- An expired or invalid license fails *closed* — the daemon boots,
  enterprise features stay off, `rousseau doctor` prints why.

### Why not the alternatives

The three obvious alternatives were considered and explicitly ruled
out. See [`memory/rousseau_agent_business_model.md`](../.claude-memory-notes)
(project-internal) for the full rationale; the short version is:

| Model | Why it's rejected |
|---|---|
| SaaS ("rousseau cloud") | Violates the core "zero telemetry, no control plane" promise, destroys the primary competitive moat with platform-ops / regulated / airgapped audiences, drags in billing hooks + multi-tenant isolation that has nothing to do with building a great agent. |
| Enterprise-only support against pure OSS | Unscalable consulting shop; trades time for ad-hoc SLAs; leads directly to maintainer burnout. |
| GPL / dual licensing | Would fragment the community and force a licence-audit cliff on every downstream consumer. Apache-2.0 / MIT + open-core is the friendlier path for platform-team adoption. |

---

## 2. The boundary — what's free, what's paid

Three surfaces are gated. Everything else stays in the core. The
seam is documented in [`internal/license/license.go`](../internal/license/license.go);
the identifiers below match the constants in that file.

### 2.1 Identity — `FeatureSSO`

| Free (core) | Paid (Team + Enterprise) |
|---|---|
| Local SQLite auth for the daemon's own operator surface | OIDC (Okta, Entra ID, Google Workspace, Auth0, …) |
| Static API keys | SAML 2.0 (for orgs that still standardise on it) |
| Inherited `claude` CLI OAuth (subscription-tier convenience) | Corporate directory sync mapping Slack / Matrix / iMessage users to internal identities |
| Cross-transport identity via `/whoami` / `/link` / `/unlink` | Group / role membership inherited from the IdP |

**Why it's gated:** enterprises pay for SSO because compliance
demands it. Small teams don't need it. The core `internal/auth/oauth`
broker (which stores tokens for outbound integrations like GitHub,
Google Workspace, Linear, Slack, Stripe) stays free — that's a
different problem from directory-based user authentication.

### 2.2 Observability — `FeatureAuditEgress`

| Free (core) | Paid (Enterprise) |
|---|---|
| Structured `slog` to stdout | Streaming audit-log egress: Splunk HEC, Datadog Logs, OTLP push, generic HTTPS sink |
| Full session history in the local SQLite DB (`sessions_fts`) | Immutable, tamper-evident log format (hash-chained records) |
| Prometheus scrape endpoint (all 15 `rousseau_*` metric families) | Extended PII / secrets redaction rule packs (industry presets: HIPAA, PCI-DSS, GDPR) |
| OpenTelemetry OTLP/HTTP tracer for spans | SIEM-ready log field mapping (CEF, LEEF) |
| Default redaction rules (`internal/observability/redact`) | Configurable retention policies with automated purge |

**Why it's gated:** compliance officers pay a premium to ensure
their team's LLM interactions ship into the same SIEM that catches
every other credential-shaped string. The base observability stack
stays free because the "you can see what your daemon is doing"
promise is what makes the OSS product trustworthy in the first
place.

### 2.3 Governance — `FeatureGovernanceAdvanced`

| Free (core) | Paid (Enterprise) |
|---|---|
| `PatternApprover` — regex allow/deny rules over tool name + input | RBAC with role hierarchies inherited from SSO groups |
| Interactive TUI approver (`y / n / a / d` per tool call) | [Open Policy Agent](https://www.openpolicyagent.org/) integration — write Rego, evaluate per tool call |
| `AllowAllApprover` / `DenyAllApprover` primitives | Multi-party approval workflows (e.g., "`terraform apply` needs one approval from the DevOps rota in Slack") |
| Lifecycle hooks in `internal/agent/hooks` | Break-glass audit trail: every override is logged with actor + justification |

**Why it's gated:** advanced governance is what an enterprise buys
when they want the OSS agent to reach production. Pattern rules +
the TUI approver are enough for a small team; multi-party approvals
and OPA rules are what a Fortune-500 compliance team demands.

---

## 3. What the license contains

The license is an Ed25519-signed JWT-like envelope (a two-part
`base64url(payload) + "." + base64url(sig)` string — no algorithm
negotiation to eliminate the `alg=none` downgrade footgun).

Payload fields (see [`Claims`](../internal/license/license.go)):

| Field | Meaning |
|---|---|
| `sub` | Opaque customer identifier (never an email or display name) |
| `tier` | `team` or `enterprise` — `core` never appears on a signed token |
| `features` | Optional explicit feature allowlist. Empty = every feature in the tier |
| `iat` | Issued-at (Unix seconds) |
| `exp` | Expiry (Unix seconds) — always set; lifetime licences are a separate contract |
| `seats` | Optional seat cap. Zero = unlimited within the licence |

Tier defaults (when `features` is empty):

- **Team** → `sso`
- **Enterprise** → `sso`, `audit_egress`, `governance_advanced`

A new feature added to a future tier is automatically unlocked for
customers on that tier — no reissue needed.

---

## 4. Operator experience

### Passing a licence

Two ways, in priority order:

```bash
# 1. environment variable — the default; containers + systemd
export ROUSSEAU_LICENSE_KEY="eyJz...<truncated>...bg"
rousseau whatsapp
```

```yaml
# 2. mode-0600 file — same-host secret-manager pattern
# rousseau reads Source{File: /etc/rousseau/license.key, Env: "-"}
```

When both are set, **env wins**. A file with looser permissions
than 0600 is rejected with a legible error — no accidental leak on
a shared box.

### `rousseau doctor` output

The doctor command prints an `identity.license` row summarising the
active licence — never the raw token. Populated fields include:
tier, subject (opaque), expiry, whether the licence is within the
14-day warn window, and a reason string when the licence isn't
active.

### Failure modes

Every failure surfaces as a `license.*` log line at `WARN` and
falls back to `tier=core`. The daemon boots regardless. The
failure taxonomy:

| Reason | Meaning | Fix |
|---|---|---|
| `no license configured` | No `ROUSSEAU_LICENSE_KEY`, no file | Add one, or accept OSS defaults |
| `bad signature` | Token signed by a key this build doesn't trust | Rebuild against the right keyring, or contact support for a reissue |
| `expired` | `exp` is before now | Renew |
| `permissive mode` | Licence file mode > 0600 | `chmod 600 /path/to/license.key` |
| `malformed payload` | Base64 decode or JSON parse failed | Copy the licence again; check for stray newlines / quoting |

---

## 5. Contributor guidance

**When adding a new feature ask "core or paid?" first.**

- If the feature is a natural extension of the OSS core (a new
  transport, a new provider, a new built-in tool, a better default,
  a performance improvement, a security fix) — it goes in the
  core, no license check.
- If the feature is one of the three gated surfaces (SSO,
  audit egress, advanced governance) OR is a feature that
  compliance officers / platform-ops buyers would pay for — it
  goes behind a `license.Checker` gate.

The gate pattern:

```go
// Somewhere in cli/ or a transport's Config assembly
if !licenseChecker.IsEnabled(license.FeatureAuditEgress) {
    return errors.New("rousseau: audit egress requires a Team or Enterprise licence — see docs/COMMERCIAL.md")
}
// … enterprise wire-up …
```

**Do not put an enterprise-shaped feature in the free core without
a gate.** A "just this once" exception erodes the boundary and
turns future upgrades into breaking changes for customers.

**Do not add SaaS-shaped work.** No billing hooks, no external
telemetry, no cross-customer isolation. `internal/tenant` is
scoped to logical workspaces within a *single on-premise
deployment*, not multi-customer SaaS.

**Documentation follows the code.** Any new gated feature updates
this document with its position on the boundary AND its constant
in `internal/license/license.go`.

---

## 6. What the OSS user always gets

To be explicit — a licence never disables anything a core user can
do. The core includes, and will continue to include:

- Nine chat transports (WhatsApp, Signal, Telegram, Matrix, Slack,
  Discord, iMessage, Email, SMS)
- Five LLM providers (Anthropic, Bedrock, Vertex, OpenAI-compatible,
  claudecli) plus the router
- Six built-in tools + 26 native integration tools (GitHub, Google
  Workspace, Slack, Linear, Stripe)
- MCP server + MCP client
- Skills / recall / sub-agent parallelism / cron scheduler
- Every hardening feature: sandbox, rate limiter, panic recovery,
  circuit breaker, redacting slog handler
- SLSA-3 releases, reproducible builds, cosign signatures
- The full session history + FTS5 search

The OSS product stands on its own. The paid tier is for the
compliance-driven upgrade path — not the price of admission.
