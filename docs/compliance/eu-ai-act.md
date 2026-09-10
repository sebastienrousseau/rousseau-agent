# EU AI Act compliance notes

**Regulation:** [EU AI Act (Regulation 2024/1689)](https://eur-lex.europa.eu/eli/reg/2024/1689/oj).
**Scope of this document:** classify rousseau-agent under the AI
Act, identify which obligations attach to which party (developer
of the underlying model, integrator, deployer), and document the
technical measures that support the operator's obligations.

**Enforcement calendar** (relevant to a rousseau-agent
deployment):

| Date | Obligation |
|---|---|
| 2 Feb 2025 | Prohibited AI practices in force (Art. 5) |
| 2 Aug 2025 | GPAI provider obligations in force (Art. 51-56) |
| 2 Aug 2026 | Rest of the Act in force; high-risk system obligations start applying |
| 2 Aug 2027 | High-risk-system obligations for pre-existing systems |

## Classification of a rousseau-agent deployment

Three parties matter under the Act:

| Party | Role | Obligations that attach |
|---|---|---|
| **The LLM provider** (Anthropic / OpenAI / Google / …) | GPAI provider (Art. 3(63) — "General-purpose AI model provider") | Art. 51-56: technical documentation, training-data summary, downstream-provider info, systemic-risk assessment if the model exceeds the FLOP threshold |
| **rousseau-agent (the software)** | GPAI system integrator — it wraps the LLM in a tool-use loop but does not train or fine-tune it | Passes the LLM provider's information downstream to the operator; supplies technical documentation for the integration itself |
| **The operator** | Deployer (Art. 3(4)) — deploys the AI system for use | Art. 26 obligations: human oversight, technical + organisational measures, monitoring, log-keeping, transparency to users |

**A rousseau-agent deployment is almost never a "high-risk AI
system" under Annex III** unless the operator specifically uses
it in a listed high-risk domain (biometric identification,
critical infrastructure, education admissions, employment
selection, essential public services, law enforcement, justice
administration, democratic processes). Typical staff-assistance
deployments are **limited-risk** (transparency obligations
apply — see Art. 50) or **minimal-risk** (voluntary code-of-
conduct only).

If your operator IS deploying rousseau in a high-risk domain
listed in Annex III, this document is insufficient; you need a
full high-risk-system conformity assessment. The maintainer
recommends against those deployments today until a first
customer has completed one and contributed learnings back.

## What the operator needs to do

### Art. 50 — Transparency obligations (limited-risk)

Applies to every AI system that "interacts with natural
persons" — every rousseau deployment. Requirements:

1. **Inform users they are interacting with an AI.** In
   practice: your bot's first message on every new
   conversation should identify itself as automated.
   rousseau's `ReplyHeader` config (`💎 *Rousseau Agent*`)
   provides a per-reply marker; add a first-turn onboarding
   message if your product design allows.
2. **Deepfake / synthetic content marking** (Art. 50(2)):
   applies only when rousseau is used to generate images,
   audio, or video (not typical) — mark those outputs as
   AI-generated per the Act's technical standards.

### Art. 26 — Deployer obligations

1. **Use as intended.** Follow the LLM provider's use
   restrictions (Anthropic's usage policy, OpenAI's usage
   policies, etc.). Do not attempt to bypass safety
   guardrails.
2. **Human oversight**: rousseau supports oversight via the
   approver chain (RBAC → OPA → MultiParty in Enterprise
   Edition). Denials + escalations are logged.
3. **Input data control**: operator-side. Filter, sanitise,
   or restrict inputs upstream if your risk assessment
   requires it.
4. **Monitoring**: `rousseau reliability` +
   `/metrics` endpoint expose the four-dimension reliability
   decomposition — treat as the ongoing monitoring surface.
5. **Logging**: automatic. Session transcripts persist to
   the state store; audit egress (Enterprise Edition) covers
   tool-call decisions.
6. **Incident notification** (Art. 79): serious-incident
   notification to the AI Office within 15 days. See the
   `../incidents/TEMPLATE.md` for the reporting structure.
7. **Data-protection impact assessment** (if GDPR Art. 35
   is triggered): see [`gdpr.md`](./gdpr.md).
8. **Register in the EU database** (if high-risk).

### Art. 4 — AI literacy

The operator is responsible for ensuring their staff have
"sufficient level of AI literacy" — training on what the
system does and doesn't do. rousseau's documentation
(README + BUYER.md + docs/) is a legitimate reference in
that training programme, but the operator's internal
onboarding is the load-bearing artefact.

### Art. 5 — Prohibited practices

**None** of rousseau-agent's shipped features cross Art. 5
lines when deployed for typical staff assistance. Practices
that WOULD cross Art. 5 lines and MUST NOT be built on top
of rousseau-agent:

- Real-time remote biometric identification in publicly
  accessible spaces (Art. 5(1)(h))
- Social scoring by public authorities (Art. 5(1)(c))
- Emotion recognition in workplace or education (Art. 5(1)(f))
- Subliminal manipulation (Art. 5(1)(a))
- Exploitation of vulnerabilities (Art. 5(1)(b))
- Untargeted scraping to create facial-recognition databases
  (Art. 5(1)(e))

If the operator's use case approaches any of these, stop and
consult an EU AI Act specialist before deploying rousseau.

## GPAI provider obligations (Art. 51-56) — NOT the maintainer's

The maintainer of rousseau-agent is NOT a GPAI provider — the
software does not train, fine-tune, or produce an AI model. The
LLM provider (Anthropic, OpenAI, etc.) is the GPAI provider and
is responsible for:

- **Art. 53**: technical documentation, training-data summary,
  distributor / downstream-provider information. Consumers of
  rousseau-agent should be able to trace this back through the
  configured provider's own AI Act documentation.
- **Art. 55**: additional systemic-risk obligations for models
  exceeding the compute threshold (currently 10^25 FLOPs
  training compute). Frontier models (Claude Opus, GPT-4 /
  GPT-5, Gemini Ultra) meet this bar; smaller local models
  (llama3, mistral) do not.

The rousseau-agent architecture merely **transports** prompts
to the GPAI system + returns responses. The AI Act does not
regulate the transport function itself.

## Technical measures rousseau-agent provides

| Requirement | How rousseau supports it | Reference |
|---|---|---|
| Human oversight (Art. 14 / Art. 26(2)) | Approver chain (RBAC → OPA → MultiParty), tool-call denials, session pause/resume | `internal/agent/approver.go`, [`../COMMERCIAL.md`](../COMMERCIAL.md) |
| Log-keeping (Art. 12 / Art. 26(6)) | SQLite session persistence + Enterprise Edition audit egress | [`gdpr.md#article-30-records-of-processing-activities`](./gdpr.md#article-30-records-of-processing-activities) |
| Accuracy + robustness + cyber-security (Art. 15) | Reliability metrics per arXiv:2602.16666, container hardening | [`../reliability.md`](../reliability.md), [`../security/sandbox.md`](../security/sandbox.md) |
| Transparency to users (Art. 13 / Art. 50) | `ReplyHeader` marker on every reply | `internal/config/config.go` |
| Data + data governance (Art. 10) | Operator-side; no training data because rousseau doesn't train | — |
| Instructions for use (Art. 13(3)) | This document + `../BUYER.md` + `../COMMERCIAL.md` + README | — |

## What your reviewer will ask

1. **"Is rousseau-agent a high-risk AI system?"** → Not on
   its own. Depends on the operator's use case (Annex III
   check). Typical staff assistance = limited-risk =
   transparency obligations only.
2. **"Who is the GPAI provider?"** → The configured LLM
   provider (Anthropic / OpenAI / …), not the maintainer.
3. **"How do we implement 'inform user they're talking to
   AI'?"** → Ship an opening message that identifies the bot;
   configure `ReplyHeader` on every reply.
4. **"What's the human-oversight mechanism?"** → Approver
   chain gates every tool call; MultiParty approval
   (Enterprise Edition) requires N-of-M sign-off for
   high-risk actions.
5. **"How do we handle a serious incident?"** →
   `../incidents/TEMPLATE.md` for the postmortem structure;
   15-day notification to the AI Office per Art. 79.
6. **"Where's your AI literacy training material?"** → Not
   provided by the maintainer — operator responsibility.
   Point your training team at README, BUYER.md, and the
   docs/ directory as reference material.

## Related

- [`README.md`](./README.md)
- [`gdpr.md`](./gdpr.md) — often paired with AI Act reviews
- [`dora.md`](./dora.md) — financial services also fall under DORA
- [`../reliability.md`](../reliability.md) — Art. 15
  accuracy + robustness telemetry
- [`../COMMERCIAL.md`](../COMMERCIAL.md) — Enterprise Edition
  RBAC / OPA / MultiParty approval for the human-oversight
  obligation
