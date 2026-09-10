# HIPAA compliance notes

**Regulation:** [US Health Insurance Portability and
Accountability Act](https://www.hhs.gov/hipaa/index.html)
(HIPAA), Privacy Rule + Security Rule + Breach Notification
Rule + HITECH.

**Scope of this document:** how a rousseau-agent deployment
inside a US healthcare covered entity or business associate
maps to HIPAA obligations, and where the software supports
those obligations. **This is not legal advice.**

**Critical read-first:** if your rousseau-agent deployment
processes electronic Protected Health Information (ePHI), you
MUST have a signed Business Associate Agreement (BAA) with
your LLM provider. The maintainer of rousseau-agent does not
sign a BAA — the maintainer does not process ePHI, does not
have a control plane, does not receive an API call from your
deployment. See "Roles" below.

## Roles under HIPAA

| Role | Who | Justification |
|---|---|---|
| **Covered entity** | The operator (healthcare provider, health plan, or clearinghouse) | Actually delivers healthcare services / processes claims |
| **Business associate** | The LLM provider — if it processes ePHI | Every prompt is forwarded to the provider by architecture |
| **Business associate** | Any MCP server or tool integration handling ePHI | Same reasoning |
| **NOT a business associate** | Sebastien Rousseau (maintainer) | Distributes software, does not process ePHI on the covered entity's behalf. Analogous to a database vendor selling an on-prem product — the vendor is not a BA for a customer's own data |

**Sign BAAs with:**
- The LLM provider (Anthropic offers a BAA for enterprise
  customers; OpenAI offers ChatGPT Enterprise + BAA; AWS
  Bedrock covers ePHI when configured via HIPAA-eligible
  services; Google Cloud offers a BAA for Vertex AI when
  Cloud Healthcare API is enabled).
- Any MCP server operator whose server receives ePHI.
- The container runtime host (your own infra — often
  self-signed BAA between internal departments if the host is
  a public cloud, that cloud has its own BAA layer).

**Do NOT sign a BAA with:**
- The rousseau-agent maintainer. Refusing the BAA ask is the
  correct legal position because the maintainer doesn't
  process ePHI — signing would be legally meaningless and
  procedurally distracting.

## HIPAA Security Rule mapping — 45 CFR § 164 subpart C

### § 164.308 — Administrative safeguards

| Standard | Operator obligation | rousseau support |
|---|---|---|
| Security management process (§ 164.308(a)(1)) | Risk analysis + risk management | Reliability metrics ([`../reliability.md`](../reliability.md)) provide continuous operational-risk telemetry |
| Assigned security responsibility (§ 164.308(a)(2)) | Designate a security official | Operator-side |
| Workforce security (§ 164.308(a)(3)) | Access authorisation + supervision | Enterprise Edition SSO + RBAC gate every session ([`../COMMERCIAL.md`](../COMMERCIAL.md)) |
| Information access management (§ 164.308(a)(4)) | RBAC / minimum-necessary | Enterprise Edition RBAC + OPA policy engine |
| Security awareness + training (§ 164.308(a)(5)) | Staff training | Operator-side; README + docs are reference material |
| Security incident procedures (§ 164.308(a)(6)) | Response + reporting | [`../incidents/TEMPLATE.md`](../incidents/TEMPLATE.md); Enterprise Edition audit egress supports forensic reconstruction |
| Contingency plan (§ 164.308(a)(7)) | Backup + disaster recovery | Operator-side; SQLite is a file, Postgres is standard |
| Evaluation (§ 164.308(a)(8)) | Periodic security assessment | Reliability metrics + `rousseau eval` synthetic-eval harness ([`../reliability.md`](../reliability.md)) |
| Business associate contracts (§ 164.308(b)) | BAAs with all BAs | Operator signs with LLM provider + MCP server operators |

### § 164.310 — Physical safeguards

Operator-side. Deploy rousseau-agent on infrastructure that
meets your physical-safeguard requirements (locked data centre
or HIPAA-eligible cloud service).

### § 164.312 — Technical safeguards

| Standard | Operator obligation | rousseau support |
|---|---|---|
| Access control (§ 164.312(a)(1)) | Unique user identification + emergency access + automatic logoff + encryption | Enterprise Edition SSO/OIDC + RBAC + audit egress; session-level allowlists via `--allow` |
| Audit controls (§ 164.312(b)) | Record + examine activity | Enterprise Edition tamper-evident (hash-chained) audit egress to Splunk / Datadog / OTLP |
| Integrity (§ 164.312(c)) | ePHI unaltered / undestroyed | SLSA-3 provenance + cosign signatures verify the shipped binary; hash-chained audit records detect tampering |
| Person / entity authentication (§ 164.312(d)) | Verify entity accessing ePHI | Enterprise Edition SSO |
| Transmission security (§ 164.312(e)) | Encryption + integrity in transit | TLS to every LLM provider + MCP server; WhatsApp E2E; other transports use the platform's native encryption |

### § 164.314 — Organisational requirements

Operator-side. BAAs, group-health-plan requirements, etc.

### § 164.316 — Policies + procedures + documentation

Operator-side. This document is one input to the operator's
own policy-and-procedure set — it doesn't substitute for
having your own written policies.

## HIPAA Privacy Rule — minimum-necessary

45 CFR § 164.502(b) requires that uses / disclosures of ePHI
be limited to the "minimum necessary" to accomplish the
purpose. rousseau supports this via:

- **Tool-level allowlisting** (Enterprise Edition OPA
  policy): "the on-call agent may read
  `patient/*/allergies.json` but not `patient/*/full-record.json`"
  becomes a Rego rule.
- **Session-level allowlisting** (`--allow` flag): only
  authenticated staff JIDs / IDs can reach the daemon.
- **Reply header** (`ReplyHeader` config): every reply
  identifies the bot, so ePHI never leaks into a "who am I
  talking to" ambiguity.
- **Redaction hooks** (operator-implementable via the
  approver chain): pre-process every prompt + response
  through a PII-detection tool call before it reaches the
  LLM.

## HIPAA Breach Notification Rule — 45 CFR § 164 subpart D

The operator's obligation, but rousseau supports it via:

- **Forensic reconstruction**: Enterprise Edition audit
  egress + reliability samples give the "who accessed what,
  when" narrative the breach investigator needs.
- **Notification timeline** (60 days for individuals; annual
  for HHS on <500 breaches; immediate for ≥500): consult your
  privacy officer; rousseau doesn't automate the notification
  itself.

## What your HIPAA reviewer will ask

1. **"Do you (rousseau maintainer) sign a BAA?"** → No.
   Distributed software, not a business associate.
   Analogous to your database vendor.
2. **"Is the LLM provider a BA?"** → Yes if it processes
   ePHI. Sign a BAA with them directly — Anthropic + OpenAI +
   AWS Bedrock + Google Vertex all offer them for enterprise
   customers.
3. **"How is ePHI encrypted at rest?"** → Operator's disk
   encryption (dm-crypt / FileVault / BitLocker). rousseau
   does not itself encrypt SQLite at the DB layer today —
   feature request open for SQLCipher support.
4. **"How is ePHI encrypted in transit?"** → TLS
   everywhere. Every transport + provider connection uses
   the platform's native TLS.
5. **"How do we audit who accessed which patient's ePHI?"**
   → Enterprise Edition audit egress hash-chains every tool
   call. Community Edition writes the same records to local
   JSONL.
6. **"How do we implement minimum-necessary?"** → Enterprise
   Edition OPA policy engine expresses "which tools + which
   patients + which staff roles" as Rego rules.
7. **"What's the incident-response procedure?"** →
   [`../incidents/TEMPLATE.md`](../incidents/TEMPLATE.md)
   for the postmortem structure; SECURITY.md SLAs for the
   vendor-side response.

## BAA-signing checklist (operator's task)

Before deploying rousseau-agent in a HIPAA-scope workflow:

- [ ] BAA signed with LLM provider(s) — Anthropic /
      OpenAI / AWS / Google
- [ ] BAA signed with hosting provider (if not on-prem)
- [ ] BAA signed with MCP server operator(s) processing ePHI
- [ ] Enterprise Edition license active (SSO + RBAC + OPA +
      audit egress are load-bearing here)
- [ ] `enable_confidence_elicitation: true` — Predictability
      signal is one of the AI reliability metrics HIPAA
      auditors want to see
- [ ] Retention policy documented; session data purged per
      the operator's retention schedule; reliability samples
      auto-prune at 30d
- [ ] `rousseau eval` fixture file authored for the operator's
      specific ePHI-handling workflows; scheduled nightly
- [ ] `docs/compliance/hipaa.md` (this file) attached to the
      operator's own HIPAA risk assessment as an appendix

## Related

- [`README.md`](./README.md)
- [`gdpr.md`](./gdpr.md) — EU-side analog for cross-border
  healthcare
- [`soc2-readiness.md`](./soc2-readiness.md) — HIPAA
  reviewers often ask for SOC 2 in the same review
- [`../reliability.md`](../reliability.md) — audit /
  monitoring surface
- [`../COMMERCIAL.md`](../COMMERCIAL.md) — Enterprise
  Edition surfaces required for HIPAA-scope deployments
