# DORA compliance notes

**Regulation:** [EU Digital Operational Resilience Act
(Regulation 2022/2554, "DORA")](https://eur-lex.europa.eu/eli/reg/2022/2554/oj).
**Applicability:** in force since **17 January 2025**. Applies
to financial entities regulated in the EU — banks, insurers,
investment firms, e-money institutions, crypto asset providers,
plus "critical ICT third-party service providers" designated by
the European Supervisory Authorities.

**Scope of this document:** how a rousseau-agent deployment
inside a financial entity fits into the DORA framework, and
where the software supports the operator's compliance
obligations. This document assumes the reader has already
mapped their organisation's DORA scope with legal counsel; the
purpose here is to give the ICT-risk / third-party-risk /
compliance team the answers they need to complete their own
paperwork.

## rousseau-agent's DORA position: ICT third-party service arrangement

DORA Article 28 requires financial entities to maintain a
register of all ICT third-party service arrangements. A
rousseau-agent deployment is one such arrangement:

- **Service function**: AI agent bridging messaging platforms
  to LLM providers.
- **ICT services provided**: software (the rousseau binary +
  the LLM sub-processor chain).
- **Criticality**: assessment depends on the operator's use
  case. Typical rousseau deployments are **non-critical /
  non-important** (Article 30(3)) — the daemon supports staff
  workflows but doesn't run authentication, execute trades,
  or hold customer funds. Formalise this decision as part of
  your own criticality assessment.

### Sub-processor chain to document in the register

For each active rousseau deployment, the register should list:

| Entity | Role | Location | Contract type |
|---|---|---|---|
| Sebastien Rousseau (maintainer) | Software supplier (not a service provider under DORA — see next section) | Not applicable — no service delivered | Open-core FSL-1.1-Apache-2.0 licence for community edition; commercial licence agreement for Enterprise Edition |
| Configured LLM provider | ICT service (LLM inference) | Per provider (Anthropic US, OpenAI US, Azure region-specific, AWS Bedrock region-specific, Vertex region-specific, local Ollama = self-hosted) | Provider's own DPA + terms |
| Configured MCP servers | ICT service (per-server function) | Per server | Per server |
| Container runtime (Podman) | Host OS component | Self-hosted | Not applicable |
| SQLite / PostgreSQL | Data storage | Self-hosted | Not applicable |

**Important distinction:** rousseau-agent is *distributed
software*, not a service. The maintainer does not process
data on the financial entity's behalf, does not receive an
API call from the deployment, does not have "customer data"
in any sense. This makes rousseau-agent structurally similar
to an on-prem database engine or an open-source library — the
DORA register entry should reflect this and NOT categorise
the maintainer as an ICT service provider.

## Article 5 — ICT risk management framework

Operator-side. rousseau supports the framework's implementation
by providing:

- **Documented architecture** — see
  [`../GAP_ANALYSIS_2026.md`](../GAP_ANALYSIS_2026.md) for
  the maintainer's own self-assessment against
  operational-resilience criteria.
- **Container security posture** —
  [`../security/sandbox.md`](../security/sandbox.md)
  documents drop-all-caps, seccomp, read-only rootfs,
  user-namespace mapping, egress restrictions.
- **Supply-chain provenance** — SLSA-3 build provenance +
  cosign signatures verify the binary matches the audited
  source.

## Article 8-15 — ICT security policies + procedures

The operator's own ICT security policy should mention
rousseau-agent under whichever policy category covers "internal
tools / AI-assisted staff workflows." Specific requirements
rousseau supports:

### Article 8 — Identification of ICT-supported functions

Document which staff functions the daemon supports (on-call
triage, PR review, deployment gate, etc). Include the LLM
provider as part of the function's dependency chain.

### Article 9 — Protection + prevention

- **Access control**: Enterprise Edition SSO/OIDC/SAML +
  RBAC + OPA policy gate every tool call. Community Edition
  provides pattern-based approver only. See
  [`../COMMERCIAL.md`](../COMMERCIAL.md).
- **Data-at-rest encryption**: operator's disk encryption
  covers SQLite + Postgres. rousseau does not itself
  encrypt at the DB layer today; feature-request for
  SQLCipher support is on the roadmap.
- **Data-in-transit encryption**: TLS to every LLM provider
  and MCP server. WhatsApp is E2E-encrypted end-to-end via
  the WhatsApp Web protocol.
- **Configuration hardening**: the rootless Podman Quadlet
  ships with DropCapability=all, NoNewPrivileges=true,
  ReadOnly=true, SeccompProfile=default, UserNS=keep-id. See
  [`../security/sandbox.md`](../security/sandbox.md).

### Article 10 — Detection

- **Continuous monitoring**: Prometheus metrics exposed via
  `/metrics` endpoint, including the four-dimension
  reliability decomposition (Consistency / Robustness /
  Predictability / Safety — see
  [`../reliability.md`](../reliability.md)) plus latency,
  cost, and error counters.
- **Audit trail**: Enterprise Edition hash-chained audit
  egress to Splunk HEC / Datadog / OTLP / HTTPS sinks. Every
  tool call gets a signed record. Community Edition writes
  the same records to local JSONL.

### Article 11 — Response + recovery

- **Automatic restart**: systemd Quadlet unit uses
  `Restart=always` with `StartLimitBurst=5` /
  `StartLimitIntervalSec=10min` so a crashing daemon comes
  back in ~10s without wedging the systemd retry queue.
- **Session isolation**: session state is per-sender; a
  crash affects only in-flight turns, never historical
  transcripts.
- **Postmortem process**:
  [`../incidents/TEMPLATE.md`](../incidents/TEMPLATE.md)
  provides the starting structure.

### Article 12 — Backup + recovery

Operator-side. SQLite state file is a standard file;
operator's backup regime handles it. Postgres is BAU. RPO/RTO
are operator-configured, not software-limited.

### Article 13 — Learning + evolving

The reliability telemetry surface (see
[`../reliability.md`](../reliability.md)) is the operator's
primary data source for "learning + evolving" — the four
dimensions from arXiv:2602.16666 are exactly the metrics that
tell you whether operational-resilience is improving or
degrading over time.

## Article 17-23 — ICT-related incident management

DORA classifies ICT incidents into three severity tiers with
mandatory reporting timelines:

| Class | Time to notify NCA | Reference |
|---|---|---|
| **Major** ICT-related incident | ≤ 24h (initial), 72h (intermediate), 1 month (final) | Article 19 |
| **Significant cyber threat** | Voluntary, "without undue delay" | Article 19(2) |
| Non-major | Internal tracking only | — |

rousseau-agent supports the operator's incident-management
process by providing:

- **72-hour forensic reconstruction capability**: the
  Enterprise Edition tamper-evident audit trail captures
  every tool call decision + outcome + actor, hash-chained so
  tampering is detectable. This is what the "intermediate
  report" and "final report" DORA obligations need.
- **Response SLA commitment**: for security-affecting bugs in
  rousseau-agent itself, the maintainer commits to ≤ 72h
  acknowledgment via [`../../SECURITY.md`](../../SECURITY.md).
- **Root-cause template**:
  [`../incidents/TEMPLATE.md`](../incidents/TEMPLATE.md)
  aligns with DORA's expectations for the intermediate + final
  reports.

## Article 24-27 — Digital operational resilience testing

DORA requires financial entities to test their operational
resilience. rousseau supports two specific test surfaces:

### Reliability metrics as continuous testing

The four-dimension decomposition
([`../reliability.md`](../reliability.md)) is a continuous
operational-resilience test running against production
traffic. R_Con / R_Rob / R_Pred / R_Saf across 30 days are
directly citable in your DORA testing evidence.

### Synthetic-eval harness (`rousseau eval`)

`rousseau eval --fixture ./eval-fixtures.yaml` runs a defined
suite of tasks with K=5 repeats + paraphrase clusters,
producing outcome-consistency and prompt-robustness samples.
Configure the fixture file to represent your critical staff
workflows and schedule a nightly cron. The Prometheus /metrics
endpoint exposes the results for your compliance dashboard.

### Threat-led penetration testing (TLPT — Article 26)

For entities designated to conduct TLPT, rousseau-agent is a
legitimate in-scope target for red-team engagements.
Coordinated disclosure via
[`../../SECURITY.md`](../../SECURITY.md); the maintainer will
accept an authorisation letter (via `security@` route) for the
TLPT engagement and pre-agree on rules of engagement.

## Article 28-30 — ICT third-party risk

The Article 28 register entry is covered above ("rousseau-agent's
DORA position"). Two additional Article 28 obligations to note:

- **Concentration risk** (Article 29): if you deploy
  rousseau-agent against a single LLM provider (e.g. Anthropic
  only), the concentration is at the LLM provider layer, not
  rousseau. Multi-provider routing (`provider: router`) lets
  you spread across Anthropic + OpenAI + Bedrock + others as
  concentration-risk mitigation.
- **Contractual arrangements** (Article 30): use the LLM
  provider's own contract; rousseau-agent's licence agreement
  (Enterprise Edition) covers the software supply side.

## Chapter V — Information + intelligence sharing

Operator-side. rousseau-agent has no built-in threat-intel
sharing today; the operator's own SIEM / SOAR / TIP is the
correct integration point.

## What your DORA reviewer will ask

1. **"Is rousseau-agent an ICT service provider under
   DORA?"** → No. It's distributed software. The LLM provider
   IS an ICT service provider. Categorise the maintainer as
   a software supplier (equivalent to a database vendor for
   an on-prem deployment).
2. **"What's the sub-processor / sub-outsourcing chain?"** →
   Documented in the "sub-processor chain" table above. Feed
   the same list into your Article 28 register.
3. **"How does the daemon handle a Sev-2 incident?"** →
   `Restart=always` for the process; audit trail (Enterprise
   Edition) for the forensics; [`../incidents/TEMPLATE.md`](../incidents/TEMPLATE.md) for the postmortem; SECURITY.md
   SLA for the vendor-side response.
4. **"How do we test operational resilience?"** →
   `rousseau eval` for scheduled synthetic testing;
   `rousseau reliability` for continuous production
   measurement; TLPT via SECURITY.md for adversarial testing.
5. **"What about concentration risk on the LLM provider?"** →
   Multi-provider routing via `provider: router` config
   spreads across Anthropic + OpenAI + Bedrock. Document
   which providers are in the failover chain.
6. **"Where's the criticality assessment?"** → Operator-side.
   Typical rousseau deployments are non-critical /
   non-important because the daemon supports staff workflows
   rather than customer-facing regulated activities. Justify
   your own classification in the register entry.

## Related

- [`README.md`](./README.md) — index + shared facts.
- [`gdpr.md`](./gdpr.md) — data-protection framework
  frequently paired with DORA reviews.
- [`soc2-readiness.md`](./soc2-readiness.md) — audit
  roadmap.
- [`../reliability.md`](../reliability.md) — telemetry
  surface referenced by Article 10, 13, 24.
- [`../../SECURITY.md`](../../SECURITY.md) — response SLAs
  cited by Article 19.
