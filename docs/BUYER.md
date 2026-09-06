# Who rousseau-agent is for

**One-page contract with the product.** Every feature decision, every
roadmap prioritisation, every marketing surface is evaluated against
the two personas below. Anything that doesn't serve them well is a
distraction — the target-buyer discipline is what stops a
"9 transports, 6 providers, tools, MCP, skills, sub-agents, A2A,
SSO, RBAC, OPA" project from becoming a nothing-for-anyone product.

Last touched: 2026-09-06. Reviewed quarterly.

## Primary persona — the regulated-enterprise CISO or platform lead

**Who:** Head of Platform / Head of Engineering / CISO / SRE lead
at a bank subject to DORA, a healthcare provider subject to HIPAA
or the EU eHealth directive, a public-sector agency subject to the
EU AI Act, a defense / intelligence contractor, a legal-services
firm, or a critical-infrastructure operator (energy, water,
telecom). Team size: 20–500 engineers. Budget authority: mid-five
to low-six figures per year for tooling.

**Their world in 2026:**

- **SaaS agent platforms are being systematically disqualified**
  by internal legal / risk teams because of data-residency,
  third-party-processor, and GDPR-transfer-mechanism concerns.
- **DORA (Digital Operational Resilience Act)** is now enforced
  across EU financial services. Every "ICT third-party service
  provider" — including AI agent SaaS — has to sit inside a
  formal risk register with contractual sub-outsourcing clauses,
  incident-reporting deadlines, and exit strategies.
- **EU AI Act** obligations kicked in through 2025 and 2026.
  General-purpose AI systems require documentation of training
  data, capability limits, and downstream integration guidance.
  Deploying a US-hosted SaaS agent that touches customer data now
  triggers an ~80-hour compliance review before procurement.
- **The team has a Slack / Signal / Matrix corporate messaging
  layer already deployed** and controlled inside their perimeter
  or on approved private cloud.
- **Existing agents are either home-grown Python scripts** cobbled
  together on internal notebooks, or **rejected SaaS trials**
  from CrewAI, LangGraph Cloud, or Cognition — none of which
  cleared internal review.

**What they need from a personal AI agent product:**

1. **Deployable inside their perimeter** — single static Go binary
   in an air-gapped rootless container. No control plane, no
   telemetry, no external key server.
2. **Enforced SSO/OIDC/SAML** — every session bound to a
   corporate identity, break-glass audit trail on any manual
   override.
3. **Policy engine (OPA/Rego)** — per-tool and per-session
   decisions the compliance team can author and review, not a
   free-text prompt.
4. **Tamper-evident audit egress** — hash-chained JSONL or
   equivalent, feeding Splunk / Datadog / OTLP / their in-house
   SIEM.
5. **Bring-your-own-LLM** — the buyer already has an Azure Foundry
   / AWS Bedrock / on-prem vLLM entitlement they need to use.
   Bundled cloud LLM SaaS is a non-starter.
6. **Provenance-verifiable releases** — SLSA-3 provenance, cosign
   signatures, reproducible builds, CycloneDX SBOM per release.
7. **Vendor of record** — a licence agreement with real support
   SLAs and a legal contact. Not a github.com/ URL and a Discord
   invite.

**What they pay:** $2,500–$5,000 per node per month at Enterprise
tier (per Nango / Ivern 2026 comparisons for adjacent self-hosted
agent platforms), typically 10–50 nodes per initial deployment.
Realistic first-contract range $30–150k ARR, with expansion into
the six-figures as the team broadens.

**Where rousseau-agent already serves this persona (v0.0.3):**

- Single static Go binary, rootless Podman Quadlet deployment,
  read-only rootfs, drop-all capabilities, seccomp, `UserNS=keep-id`.
- 6 LLM providers including Bedrock and Vertex (BYO-LLM ✅).
- SLSA-3, cosign-signed images, reproducible builds, SBOM per
  release. CI publishes for 12 GOOS/GOARCH combinations.
- Enterprise Edition surfaces (SSO, audit egress, RBAC + OPA)
  present in the tree, gated by an offline Ed25519 licence key.
  Same binary as Community — the licence toggles the features.
- Zero telemetry, zero control plane.

**Where rousseau-agent still has work to do:**

- **SOC 2 Type II** — readiness assessment planned Phase 3.
- **DORA / EU AI Act / HIPAA compliance docs** — planned Phase 3
  (`docs/compliance/` directory).
- **Sales-ready case study** with a named regulated buyer —
  Phase 5.
- **Official-path WhatsApp** (Meta BSP-brokered, official
  templates) — Phase 6.

## Secondary persona — the sovereignty-first prosumer

**Who:** Privacy-focused engineer running a homelab; indie hacker
or solo consultant on a personal server; developer at a small
startup that wants agent tooling without SaaS lock-in; oncall
engineer who wants to hand queries to an agent from Signal at
2am; ML researcher with an air-gapped GPU box.

**Their world in 2026:**

- **Willingness-to-pay ceiling is ~$20/month**, anchored by
  ChatGPT Plus / Claude Pro / Gemini Advanced.
- **Distribution channels are HN, Product Hunt, r/selfhosted,
  r/programming, r/golang, Lobsters, and specific Discord servers**
  — not enterprise procurement.
- **They value the "one binary, no cloud dependency" story
  intensely** and are the loudest amplifier if the product
  delivers on it.
- **They will not pay Enterprise-tier prices** but will contribute
  bug reports, feature PRs, and (most valuably) case studies and
  word-of-mouth referrals into their day-jobs.

**What they need:**

1. **Five-minute time-to-first-message.** `curl | sh` install,
   sensible defaults, one WhatsApp QR scan or Signal PIN, done.
2. **Runs on a Raspberry Pi 5 / N100 mini-PC / homelab NAS** in
   under 100 MB RAM idle.
3. **Bring-your-own-Claude / Anthropic / OpenAI key** — they
   already have subscriptions and don't want an intermediary
   billing.
4. **Never breaks after an update.** Reversible, auditable, no
   silent behaviour changes.
5. **Extension points that don't require rewriting the daemon** —
   Skills, MCP servers, cron schedules, integrations.

**What they contribute:**
- GitHub stars (currently 1, target 500+ by Y1, 5k+ by Y2).
- HN front-page hits on release-milestone blog posts.
- Signal-boosting into the enterprise persona's day job.
- Occasional PRs (small, but valuable trust signal on the repo).

**Where rousseau-agent already serves this persona:**

- Single binary + rootless container.
- BYO-key model across 6 providers.
- Cron scheduler, MCP client, Skills package.
- CI + reproducible-build discipline that means updates don't
  surprise.

**Where rousseau-agent still has work to do:**

- **`curl | sh` installer** — Phase 4.1. Currently a 30-minute
  Quadlet + QR + Claude CLI OAuth yak-shave.
- **Interactive `rousseau setup` wizard** — Phase 4.1.
- **Documentation site with search** — Phase 4.5.

## Personas we explicitly do NOT serve

Naming what we're not is as important as naming what we are —
these are recurring feature requests that must be politely
declined so the two primary personas stay served well.

- **"Prosumer wanting a hosted managed service."** rousseau is
  self-hosted; that's the whole point. Anyone who wants a hosted
  agent has 15 SaaS options starting at $20/month. Adding a
  managed cloud offering would forfeit the sovereignty story
  that makes both primary personas care.
- **"Enterprise wanting drag-and-drop skill authoring in a
  web UI."** Skills are markdown files. The market picked
  VS Code + the agentskills extension as the authoring UI. We
  will not rebuild a worse version.
- **"AI-hobbyist wanting a multi-agent orchestration
  framework."** LangGraph, CrewAI, and Microsoft Agent Framework
  own that surface. rousseau's sub-agent primitive is
  orchestrator-worker (one lead, N ephemeral isolated workers),
  which is the pattern that won in 2026 — but the product isn't
  a framework for building bespoke topologies.
- **"Enterprise wanting 24/7 phone support with a 15-minute
  SLA."** Solo-maintained today, ~5–10-person team at Enterprise
  maturity. Support tiers will exist — a same-day phone-support
  SLA won't be one of them until the team is materially larger.

## Non-persona features that keep showing up

Track them here so the pattern is visible; decline politely
citing this doc:

- Slack app for the Slack Marketplace (Slack SaaS-only, wrong
  cohort).
- Chrome/Firefox extension (browser-tab shape is wrong for a
  daemon-first product).
- Web dashboard for viewing conversation history (bad-for-privacy
  attractor; the state is in SQLite and any admin who needs it
  can query directly).
- Cloud-hosted "free tier" (see "personas we do NOT serve").
- Multi-agent negotiation protocol beyond A2A (LangChain / CAMEL
  own this and it isn't near the primary personas' needs).

## Related

- [COMMERCIAL.md](./COMMERCIAL.md) — the business model that
  serves these personas.
- [COMPETITORS.md](./COMPETITORS.md) — who each persona is
  currently choosing between.
- [ROADMAP.md](./ROADMAP.md) — the delivery plan that closes the
  gaps called out above.
- [LICENSE-RATIONALE.md](./LICENSE-RATIONALE.md) — the license
  chosen to serve the primary persona's procurement team.
