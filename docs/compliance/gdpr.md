# GDPR compliance notes

**Regulation:** [EU General Data Protection Regulation (GDPR)](https://eur-lex.europa.eu/eli/reg/2016/679/oj).
**Scope of this document:** how the rousseau-agent architecture
aligns with GDPR obligations that fall on **operators** deploying
it, and how the maintainer supports those obligations at the
software level.

**This is not legal advice.** Every deployment of rousseau-agent
processes real personal data; a data-protection lawyer in your
jurisdiction is the right source of "does this satisfy your
Article X obligation." This document tells your lawyer what the
software does and where in the code they can verify it.

## Roles under GDPR

| Role | Who | Justification |
|---|---|---|
| **Data controller** | The operator deploying rousseau-agent | Chooses the purposes and means of processing — which users can message the daemon, which LLM provider gets the transcripts, what tools the agent can run. |
| **Data processor** | The maintainer (Sebastien Rousseau) as the software supplier | Provides the software but does not process personal data on the operator's behalf. No control plane. No telemetry. No cloud service. |
| **Sub-processor** | The configured LLM provider (Anthropic / OpenAI / Bedrock / Vertex / Ollama), plus any MCP servers or tool-integration APIs the operator has enabled | Every prompt is forwarded to the LLM provider by architecture — that provider is a sub-processor and the operator's own DPA with the provider covers it. |

**Corollary:** rousseau-agent is *distributed software*, not
processing-as-a-service. The maintainer signs no DPA and holds no
data. Analogous to how a database vendor doesn't sign a DPA for a
customer's on-prem Postgres deployment — the vendor is the
supplier, not the processor.

## Article-by-article: what the operator needs to do, what rousseau does for them

### Article 5 — Principles

- **Lawfulness / fairness / transparency**: operator-side —
  ensure your users have consented (or have another Article 6
  basis) to being messaged by an AI agent and having their
  content forwarded to the LLM provider.
- **Purpose limitation**: enforced at deployment. rousseau-agent
  processes messages only for the purposes the operator's
  agent-loop tools implement.
- **Data minimisation**: enforced at deployment. Turn off
  transports you don't use; scope the `ROUSSEAU_WHATSAPP_ALLOW`
  allowlist to specific JIDs; use `agent.max_iterations` to cap
  runaway agent loops.
- **Accuracy**: LLM-generated content is not guaranteed
  accurate; the operator's product design must handle
  correction (Article 16) — see `rousseau session delete` and
  the `Right to erasure` section below.
- **Storage limitation**: operator-side — set retention policies
  on the sessions table. rousseau's own reliability samples are
  pruned at 30 days by default (see
  [`../reliability.md`](../reliability.md)).
- **Integrity / confidentiality**: rousseau ships with
  container hardening (rootless Podman, drop-all-caps, seccomp,
  read-only rootfs — see
  [`../security/sandbox.md`](../security/sandbox.md)),
  SLSA-3 provenance, cosign-signed releases, reproducible
  builds. See [`soc2-readiness.md`](./soc2-readiness.md) for
  the mapped Security Trust Services Criteria.
- **Accountability**: this document + the
  [`../reliability.md`](../reliability.md) audit surface + the
  Enterprise Edition tamper-evident audit egress support the
  operator's accountability obligation.

### Article 6 — Lawful basis

Operator-side. rousseau doesn't presume a basis; typical
deployments use **legitimate interest** (internal-tools use case)
or **consent** (customer-facing use case). Document the chosen
basis in your own record of processing activities.

### Article 12-14 — Transparency

Operator-side. Your privacy notice needs to disclose:

1. That an AI agent may respond to messages.
2. Which LLM provider(s) receive the content (Anthropic, OpenAI,
   etc. — check your `provider` config).
3. Where the data is stored (`state.dsn` path — usually
   operator-controlled hardware).
4. Retention period (session retention is operator-configurable;
   reliability samples auto-purge at 30d).

### Article 15 — Right of access

Operator produces the data extract. Two commands:

```bash
# All sessions where this JID / user was involved.
sqlite3 sessions.db \
  "SELECT id, title, created_at, updated_at, payload
   FROM sessions
   WHERE sender = ?" -- <e164-phone-number>

# All reliability samples (metadata only — no message content).
sqlite3 sessions.db \
  "SELECT at, dimension, sub_metric, value, metadata
   FROM reliability_samples
   WHERE session_id = ?" -- <session-uuid>
```

The `payload` column is the JSON conversation history — hand it
over as the "personal data extract."

### Article 16 — Right to rectification

Operator-side application logic. rousseau exposes the raw
sessions table; correction is UPDATE on the row.

### Article 17 — Right to erasure

rousseau ships a first-class delete-by-sender workflow.

```bash
# Delete every session originating from this WhatsApp / SMS / iMessage
# phone number. Cascades to every message in those sessions.
rousseau session delete-by-sender 15551234567@s.whatsapp.net

# Confirm no rows remain.
sqlite3 sessions.db \
  "SELECT count(*) FROM sessions WHERE sender = '15551234567@s.whatsapp.net'"
# Expect: 0
```

For reliability samples that reference the same session ID:
```
sqlite3 sessions.db "DELETE FROM reliability_samples WHERE session_id IN
  (SELECT id FROM sessions WHERE sender = ?)"
```

For audit-egress records already forwarded to a SIEM: handled at
the SIEM tier via the operator's own retention/redaction tooling.
rousseau does not persist audit records locally after forwarding
(see [`../security/sandbox.md`](../security/sandbox.md#audit-egress)).

### Article 20 — Right to portability

The `sessions.payload` JSON is a machine-readable structured
format. Hand it over as-is; the recipient can import it into any
tool that understands JSON.

### Article 25 — Data protection by design + by default

Design decisions in rousseau-agent that support Article 25:

1. **Allowlists first.** Every transport supports a `--allow`
   flag that silently drops messages from non-listed senders.
   The default is explicitly *no* allowlist (documented as
   "dangerous — do not run on a public number"), so the
   operator must actively make the deployment public.
2. **Local-first storage.** SQLite state file lives under
   `$XDG_DATA_HOME/rousseau/` (operator's home dir); Postgres
   support is a bring-your-own-cluster option. No cloud state.
3. **No telemetry.** See `docs/compliance/README.md#where-the-data-does-not-go`
   for the verified-in-code claim.
4. **Explicit approver chain.** Every tool call passes through
   the approver interface (RBAC → OPA → MultiParty in Enterprise
   Edition). Denials are recorded as Safety violations in
   reliability samples.
5. **Data-minimising reliability metrics.** Reliability samples
   record latency, token counts, tool-call counts, and
   success/failure — **no message content, no user identifiers
   beyond session UUID.**

### Article 30 — Records of processing activities

Operator-side. A template `Article 30 Record` for a
rousseau-agent deployment:

- **Name of controller:** [your organisation]
- **Contact of DPO / privacy office:** [your contact]
- **Purpose of processing:** [e.g. "internal on-call
  assistance", "customer support automation"]
- **Categories of data subjects:** [e.g. "employees", "existing
  customers who opted in"]
- **Categories of personal data:** identifiers (JID / user ID /
  email address), message content, timestamps
- **Recipients:** the configured LLM provider (name + region)
- **Third-country transfers:** yes if provider is
  US-hosted; document the transfer mechanism (SCCs, adequacy
  decision)
- **Retention:** [operator-configured; default = indefinite
  in `sessions.db` until manual delete; 30 days in
  `reliability_samples`]
- **Technical measures:** rootless container, drop-all-caps
  seccomp filter, encryption in transit (TLS to LLM
  provider), encryption at rest (operator's disk encryption)

### Article 32 — Security of processing

- **Pseudonymisation / encryption:** operator's disk encryption
  + TLS to LLM provider. rousseau does not encrypt SQLite at
  rest on its own (relies on disk-layer encryption); operators
  needing SQLCipher-style DB-level encryption should file a
  feature request.
- **Confidentiality / integrity / availability / resilience:**
  addressed by container hardening
  ([`../security/sandbox.md`](../security/sandbox.md)),
  the Enterprise Edition's tamper-evident audit egress, and
  the [`../reliability.md`](../reliability.md) reliability
  telemetry.
- **Ability to restore:** SQLite state file is a normal file;
  operator's backup regime handles restore.
- **Regular testing:** CI runs vet + lint + govulncheck + race
  tests + fuzz tests + coverage-gate on every commit; SLSA-3
  provenance verifies build integrity.

### Article 33-34 — Breach notification

Operator responsibility. rousseau supports the operator's
notification obligation via:

- Enterprise Edition audit egress: every tool-call decision is
  hash-chained and forwarded to the operator's SIEM,
  supporting the "72-hour forensic reconstruction" a breach
  notification requires.
- [`../../SECURITY.md`](../../SECURITY.md) response SLAs
  commit the maintainer to ≤ 72 hours acknowledgment of
  security reports.
- [`../incidents/TEMPLATE.md`](../incidents/TEMPLATE.md)
  gives the operator a starting postmortem structure.

### Article 35 — DPIA

A **Data Protection Impact Assessment** is likely required for
any deployment that:

- Processes special-category data (Article 9 — health, race,
  religion, etc.) — HIPAA-scope healthcare deployments always.
- Systematically monitors publicly accessible spaces on a large
  scale.
- Processes children's data.

If your deployment triggers a DPIA, the "technical measures"
section of this doc feeds into it. Attach a copy of this
document as an appendix to the DPIA.

## Data-flow diagrams by transport

### WhatsApp (whatsmeow)

```
User's phone (WhatsApp app)
   ↓ E2E-encrypted via WhatsApp Web protocol
Meta / WhatsApp servers
   ↓ over WSS to the linked-device (rousseau daemon)
rousseau-agent daemon (in operator's process)
   ↓ prompt bytes over HTTPS
Configured LLM provider (Anthropic / OpenAI / Bedrock / …)
   ↓ response bytes
rousseau-agent daemon
   ↓ back through WSS to WhatsApp servers
Meta / WhatsApp servers
   ↓
User's phone
```

Sub-processor list for this transport: **Meta (WhatsApp)** +
configured **LLM provider**. Both need their own DPAs with the
operator.

### Signal, Telegram, Discord, Matrix, Slack, iMessage (BlueBubbles), Email (IMAP/SMTP)

Same shape — replace "Meta / WhatsApp" with the respective
transport's control server. Signal is E2E and does not route
through a Signal-owned relay for the message body (only for
metadata). Matrix depends on the homeserver the operator
picks; on-prem homeserver eliminates the third-party middle
hop entirely.

### SMS (Twilio / Vonage / similar BSP)

Outbound-only. The chosen BSP is a sub-processor. Twilio has
GDPR-adequacy documentation the operator's DPO should already
have on file for other Twilio use cases.

## What your reviewer will ask

1. **"Who is the data controller / processor?"** → Operator is
   controller; maintainer is the software supplier not a
   processor; LLM provider is sub-processor. Refuse any DPA
   ask directed at the maintainer — it's the wrong entity.
2. **"Where is the data hosted?"** → Wherever the operator
   deployed the daemon (their infra) + wherever the LLM
   provider serves from (documented per-provider).
3. **"Can we get a copy of your SOC 2 / ISO 27001?"** → See
   [`soc2-readiness.md`](./soc2-readiness.md) — audit roadmap
   documented; certification not yet held.
4. **"How do we delete a user's data on request?"** →
   `rousseau session delete-by-sender <ID>`. Cascades to all
   messages. Reliability samples auto-purge or can be
   manually deleted with one SQL statement.
5. **"How do we know the software does what you claim?"** →
   Every claim in this document has a `file:line` citation
   into the codebase. SLSA-3 provenance verifies the released
   binary was built from the reviewed source. Cosign
   signatures verify the binary hasn't been tampered with in
   transit.
6. **"What happens if you (the maintainer) get compromised?"**
   → Cosign keyless signatures use ephemeral keys via the
   transparency log — a compromised maintainer cannot
   retroactively sign a malicious release without a public
   log entry. `docs/security/sandbox.md` documents the
   process. Update policy: only recent releases receive
   security fixes (see `../../SECURITY.md`).

## Related

- [`README.md`](./README.md) — index + shared facts.
- [`soc2-readiness.md`](./soc2-readiness.md) — controls
  mapping.
- [`../reliability.md`](../reliability.md) — telemetry surface
  referenced by Article 32.
- [`../security/sandbox.md`](../security/sandbox.md) —
  container hardening.
