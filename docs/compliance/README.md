# Compliance documentation

**Status:** structured for enterprise procurement review. Every
document in this directory is intended to be handed to an
internal compliance / legal / risk team as the starting point of
their assessment — not as a substitute for their assessment.

## What lives here

| File | Regulation / framework | Primary buyer |
|---|---|---|
| [`gdpr.md`](./gdpr.md) | EU General Data Protection Regulation | Any EU-facing deployment |
| [`dora.md`](./dora.md) | EU Digital Operational Resilience Act (2025 enforced) | EU financial services |
| [`eu-ai-act.md`](./eu-ai-act.md) | EU AI Act (Regulation 2024/1689) | Any EU-facing AI deployment |
| [`hipaa.md`](./hipaa.md) | US Health Insurance Portability and Accountability Act | US healthcare providers + business associates |
| [`soc2-readiness.md`](./soc2-readiness.md) | AICPA Trust Services Criteria (SOC 2 Type II) | Any enterprise procurement RFP |
| [`../incidents/TEMPLATE.md`](../incidents/TEMPLATE.md) | Postmortem template | Internal incident review |

## Honest scope statement

- **What these documents ARE:** the maintainer's good-faith
  assessment of how the software architecture aligns with each
  framework's requirements, backed by specific `file:line`
  citations into the codebase, gap analyses where the software
  falls short, and templates operators can adapt.
- **What these documents are NOT:** legal advice, an attorney
  opinion, an auditor's letter, a certification, or a
  regulator-blessed compliance package. rousseau-agent has not
  (yet) undergone third-party audit for any framework listed
  here — the roadmap for that is in
  [`soc2-readiness.md`](./soc2-readiness.md).
- **What you should do with them:** send them to your internal
  compliance / legal team as the starting point of their review.
  Every doc closes with a "questions your reviewer will ask"
  section — those are the load-bearing conversations to have.

## Common data-protection facts (referenced by every doc below)

These are the primitives every regulation asks about, presented
once so the individual regulation docs don't duplicate.

### What data flows through rousseau-agent

Three categories of processing:

1. **Chat transport traffic** — inbound messages from the user's
   own accounts on WhatsApp / Telegram / Signal / Discord /
   Matrix / Slack / iMessage / Email / SMS, and the daemon's
   outbound replies to the same accounts.
2. **LLM provider traffic** — every user message is (by
   architecture) forwarded to the configured LLM provider
   (Claude CLI, Anthropic API, OpenAI, Bedrock, Vertex, or a
   local Ollama endpoint). The provider is a downstream
   sub-processor.
3. **Tool-invocation side-effects** — bash execution, file
   reads/writes, MCP tool calls to whatever servers the operator
   configured, HTTP calls the agent chose to make.

### Where the data lives

- **Session history:** SQLite (default) or PostgreSQL (HA), path
  configured via `state.dsn`. Default location
  `$XDG_DATA_HOME/rousseau/sessions.db`. Contents: user
  messages, model replies, tool inputs, tool outputs, per-turn
  cost telemetry.
- **Transport auth credentials:** whatsmeow session file
  (`whatsapp.db`), signal-cli config, Discord token, Slack
  token, Matrix access token, IMAP/SMTP creds — all bind-mounted
  from the operator's own filesystem (`~/.local/share/rousseau/`
  or equivalent). rousseau never phones these home.
- **LLM provider credentials:** operator-supplied via env or
  config; rousseau reads and forwards, never persists beyond
  process lifetime unless the operator writes them into
  `config.yaml`.
- **Reliability samples:** SQLite `reliability_samples` table
  under 30-day rolling retention (see
  [`../reliability.md`](../reliability.md)). Contents:
  per-turn latency, token count, tool-call count, success/
  failure, approver denials (severity metadata), confidence
  scores when elicitation is on. **No message content.**
- **Audit egress records** (Enterprise Edition, `governance_
  advanced` license): tamper-evident JSONL of every tool-call
  decision, forwarded to a Splunk HEC / Datadog / OTLP / HTTPS
  sink the operator configures.

### Where the data does NOT go

- **No control plane.** rousseau-agent has no rousseau-owned
  cloud service. Operator deploys on their own hardware.
- **No telemetry.** No usage-metrics phone-home. Verified: grep
  the tree for `http.Post`, `http.Get`, `http.NewRequest` —
  every match is either an operator-configured integration
  (Slack API, GitHub API, MCP HTTP client) or a downstream LLM
  provider the operator explicitly enabled.
- **No auto-update.** The daemon does not fetch or install
  updates on its own; operator runs `make deploy` or the
  container image bump.
- **No opt-out required.** All of the above is the default and
  only behaviour. There is no "share diagnostics" toggle.

### Right to erasure — implementation

For the four transport identifiers rousseau natively tracks:

- **WhatsApp / Signal / iMessage / SMS**: delete all sessions
  for a phone number:
  ```
  rousseau session delete-by-sender <e164-phone-number>
  ```
- **Telegram / Discord**: same command with the platform user
  ID.
- **Email**: same command with the email address.

Reliability samples are automatically pruned by the 30-day
retention loop; no manual purge is required for those unless an
operator wants an immediate delete:
```
sqlite3 sessions.db "DELETE FROM reliability_samples WHERE session_id = ?"
```

Audit-egress records (Enterprise Edition) are typically forwarded
to a SIEM the operator controls; delete-by-subject is handled at
that layer via the SIEM's own retention/redaction tooling.

Full worked example with SQL: [`gdpr.md#right-to-erasure`](./gdpr.md#right-to-erasure).

### Data residency

100% on operator infrastructure. rousseau-agent as a product
does not choose where the data lives — the operator does, when
they select a `state.dsn` path or Postgres cluster. LLM
provider selection is the one place the data may cross
regional boundaries (e.g. Anthropic's US-hosted API), and
operators wanting EU-only residency should pair rousseau with
either a local `ollama` endpoint or an EU-region deployment of
Bedrock / Vertex.

## Related

- [`../BUYER.md`](../BUYER.md) — who these compliance docs
  primarily serve.
- [`../COMMERCIAL.md`](../COMMERCIAL.md) — Enterprise Edition
  gating (SSO, RBAC, OPA, signed audit egress).
- [`../reliability.md`](../reliability.md) — reliability
  telemetry surface referenced by SOC 2 controls.
- [`../security/sandbox.md`](../security/sandbox.md) —
  container hardening the compliance docs cite.
- [`../../SECURITY.md`](../../SECURITY.md) — vulnerability
  reporting, response SLAs, trust model.
