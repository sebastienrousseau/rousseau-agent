# SOC 2 Type II readiness

**Framework:** [AICPA Trust Services
Criteria](https://us.aicpa.org/interestareas/frc/assuranceadvisoryservices/aicpasoc2report.html) —
the industry-standard controls framework used by SaaS buyers'
procurement teams as a proxy for "this vendor takes security
seriously."

**Status of rousseau-agent as a certified party:** **not yet
audited.** This document is the maintainer's controls-mapped-to-
code walkthrough, intended to serve two purposes:

1. Give operators the evidence + citations to complete their
   own SOC 2 audit when they include rousseau-agent as an
   in-scope component of their environment.
2. Publicly document the roadmap toward a rousseau-agent
   SOC 2 Type II report for the Enterprise Edition.

**This is not a SOC 2 report.** Only an accredited CPA firm
can issue one, following the six-to-twelve-month observation
window the framework requires. The plan for that observation
is at the bottom of this document.

## Trust Services Criteria — rousseau architecture mapping

SOC 2 covers five criteria; a typical enterprise buyer asks for
**Security** as a minimum, often adds **Availability** and
**Confidentiality**, and sometimes adds **Processing Integrity**
+ **Privacy** for regulated verticals.

### Common Criteria (CC) — Security

| # | Criterion | rousseau architecture | Evidence |
|---|---|---|---|
| CC1.1 | Integrity + ethical values | FSL-1.1-Apache-2.0 licence, DCO on every commit, open-source contributor community | [`../LICENSE-RATIONALE.md`](../LICENSE-RATIONALE.md), [`../../CLA.md`](../../CLA.md), git log |
| CC1.2 | Board oversight | Solo-maintained today; formal governance model added when the enterprise-tier customer base warrants it | Roadmap: [`../ROADMAP.md`](../ROADMAP.md) |
| CC2.1 | Communication of security policies | This directory + `SECURITY.md` + `CONTRIBUTING.md` | `docs/compliance/`, [`../../SECURITY.md`](../../SECURITY.md), [`../../CONTRIBUTING.md`](../../CONTRIBUTING.md) |
| CC2.2 | Communication with external parties | Public docs, GitHub issue tracker, `security@` reporting channel | GitHub repo + SECURITY.md |
| CC3.1 | Risk identification | Reliability metrics (four-dimension decomposition), continuous CVE scanning via `govulncheck` | [`../reliability.md`](../reliability.md), `.github/workflows/ci.yml` |
| CC4.1 | Monitoring | Prometheus `/metrics` endpoint exposes latency, cost, reliability + violation counters | `internal/observability/metrics.go`, `internal/reliability/prometheus.go` |
| CC5.1 | Control activities | Approver chain (RBAC → OPA → MultiParty) gates every tool call; audit egress records every decision | `internal/agent/approver.go`, `internal/agent/approver_reliability.go`, `internal/observability/audit_egress/` |
| CC6.1 | Logical access — provisioning | Enterprise Edition SSO/OIDC/SAML brokered to every session | [`../COMMERCIAL.md`](../COMMERCIAL.md), `internal/auth/sso/` |
| CC6.2 | Logical access — registration | SCIM 2.0 pull-based directory sync (Enterprise Edition) | `internal/auth/scim/` |
| CC6.3 | Logical access — modification | RBAC role-hierarchy resolver; role changes propagate immediately | `internal/auth/rbac/` |
| CC6.6 | Access to system components | Rootless Podman container: DropCapability=all, NoNewPrivileges, ReadOnly rootfs, seccomp | [`../security/sandbox.md`](../security/sandbox.md), `docker/rousseau-agent.container` |
| CC6.7 | Data transmission | TLS to every LLM provider + MCP server + audit-egress sink | grep the tree for `http.Client` / `tls.Config` |
| CC7.1 | System operations — malware detection | Container runs from cosign-signed image; runtime seccomp filter; process isolation via Podman userns | `.github/workflows/container-release.yml` |
| CC7.2 | System operations — anomaly detection | Reliability metrics; per-tool circuit breakers via resilience package | `internal/reliability/`, `internal/resilience/` |
| CC7.3 | System operations — incident response | SECURITY.md SLAs; postmortem template; audit-egress forensic trail | [`../../SECURITY.md`](../../SECURITY.md), [`../incidents/TEMPLATE.md`](../incidents/TEMPLATE.md) |
| CC7.4 | Change management — CI | Every merge runs vet + lint + govulncheck + race tests + fuzz + coverage-gate at 95% + reproducible-build check | `.github/workflows/ci.yml`, `scripts/coverage-gate.sh` |
| CC7.5 | Change management — release | SLSA-3 provenance + cosign keyless signatures + CycloneDX SBOM per release | `.github/workflows/slsa.yml`, `.github/workflows/container-release.yml` |
| CC8.1 | Vendor management | LLM providers, MCP servers, transport BSPs treated as sub-processors; documented per-provider in [`gdpr.md`](./gdpr.md) | [`gdpr.md`](./gdpr.md), [`dora.md`](./dora.md) |
| CC9.1 | Backup / recovery | Operator-side (SQLite is a file, Postgres is standard) | — |
| CC9.2 | Vendor + partner monitoring | `govulncheck` weekly against every direct + indirect dependency; version-pinned `go.mod` | `go.mod`, `.github/workflows/ci.yml` |

### Availability

| # | Criterion | Evidence |
|---|---|---|
| A1.1 | Capacity + scaling monitoring | Prometheus counters + histograms; per-provider circuit breakers |
| A1.2 | Environmental protections | Operator-side (host + power + network are operator's responsibility) |
| A1.3 | Backup + recovery | Operator-side; SQLite file + Postgres are standard |

### Confidentiality

| # | Criterion | Evidence |
|---|---|---|
| C1.1 | Confidentiality of transmission + storage | TLS in transit; operator-managed disk encryption at rest; Enterprise Edition licence-gated features never leak between licence tiers (verified by `internal/license/`) |
| C1.2 | Disposal of confidential info | `rousseau session delete-by-sender` + auto-pruning reliability samples + Enterprise Edition audit-egress retention configurable at the SIEM tier |

### Processing Integrity (optional)

| # | Criterion | Evidence |
|---|---|---|
| PI1.1 | Data accurately + completely captured | Reliability Consistency C_res + C_out metrics |
| PI1.2 | Data validated before processing | Approver chain rejects malformed tool inputs; PatternApprover pre-flight regex over tool inputs |
| PI1.3 | Processing produces expected results | Reliability Predictability (Brier / calibration / AUROC) surface real vs claimed confidence |
| PI1.5 | Processing outputs available in expected timeframe | Prometheus latency histograms per provider per operation |

### Privacy (optional — pairs with GDPR)

See [`gdpr.md`](./gdpr.md). Every Privacy criterion maps to
either a GDPR article covered there or a Confidentiality
criterion above.

## Roadmap to a rousseau-agent SOC 2 Type II report

The maintainer's plan to achieve a rousseau-agent SOC 2 Type II
report for the Enterprise Edition:

### Phase 0 — Readiness assessment (planned Q1 2027)

Engage a SOC 2 automation platform (Drata, Vanta, Secureframe).
Complete the readiness assessment against the Common Criteria
above. Expected outcome: >85% control coverage from the
existing architecture; remaining gaps documented + closed
before the observation window starts.

**Cost estimate:** $7-15k/yr platform + ~30 hours of
maintainer time answering questionnaires + gathering evidence.

### Phase 1 — Observation window (6-12 months)

Once the readiness gaps are closed, start the auditor's
observation window. The platform automates evidence
collection continuously; the maintainer's job is to keep the
controls running (which the architecture already does).

### Phase 2 — Type II audit (3-6 weeks)

Accredited CPA firm reviews the evidence + issues the report.
**Cost estimate:** $15-40k for a first Type II audit
(midsize firm; big-four firms are 2-3x).

### Phase 3 — Public report distribution

SOC 2 Type II reports are typically NDA-shared with
prospective customers (not posted publicly) so they include
detail like specific control failures + remediation status.
Enterprise Edition customers get access via a signed NDA.

### Total budget

$22-55k in year 1 to reach a first Type II report. The
maintainer commits to sharing the report publicly once
issued, redacted per standard practice.

## What your reviewer will ask

1. **"Do you have a SOC 2 report?"** → Not yet.
   Readiness assessment on the roadmap for Q1 2027;
   report expected end of 2027. In the meantime, this
   document maps controls to code so your auditor can
   independently verify.
2. **"Are you working with Vanta / Drata / Secureframe?"**
   → Not yet — commercial trigger. Enterprise Edition
   customer commitment (≥ 3 named customers) triggers
   Phase 0 spend.
3. **"What do we do about the Vendor Security
   Questionnaire in the meantime?"** → Complete it
   citing this document + specific `file:line` references
   into the codebase. The claims are independently
   verifiable via `git blame` + reading the linked
   modules.
4. **"Is there any independent security review of the
   code?"** → `govulncheck` runs weekly against every
   dependency; SLSA-3 provenance verifies the build; no
   third-party pen-test yet (roadmap: paired with the SOC 2
   observation window).
5. **"Can we do our own pen test?"** → Yes.
   Coordinated per SECURITY.md — email
   `security@` with the engagement letter + scope
   pre-arranged rules of engagement.

## Related

- [`README.md`](./README.md) — index + shared facts
- [`gdpr.md`](./gdpr.md) — EU parallel review
- [`dora.md`](./dora.md) — EU financial services parallel review
- [`hipaa.md`](./hipaa.md) — US healthcare parallel review
- [`../../SECURITY.md`](../../SECURITY.md) — SLAs cited by CC7.3
- [`../reliability.md`](../reliability.md) — telemetry
  cited by CC3.1, CC4.1, PI1.1, PI1.3
