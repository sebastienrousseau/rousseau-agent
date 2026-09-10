# Why not just use Hermes Agent?

**TL;DR:** Hermes Agent (Nous Research) is the right answer if you
want the broadest messaging surface in the space (~25 platform
adapters), a mature Skills marketplace with agent self-authoring,
seven sandboxing backends including serverless (Modal / Daytona /
Vercel), and the DX of a Python 3.11 project you install with
`curl | bash`. rousseau-agent is the right answer if you want a
single static Go binary, container-native deployment,
provenance-verifiable releases, license-gated Enterprise Edition
surfaces (enforced SSO, RBAC, OPA policy, signed tamper-evident
audit egress), and a "same signed binary you already audited"
supply-chain story your procurement team can sign off on.

They're both "personal AI daemon you run on your own hardware,"
but Hermes is optimising for prosumer + researcher reach and rousseau
is optimising for regulated-enterprise governance. The answer to
"which is better?" depends entirely on which of those two you are.

## What Hermes Agent is

[Hermes Agent](https://hermes-agent.org) is the flagship open-source
agent from [Nous Research](https://nousresearch.com), a well-known
open-model lab. Repository:
[github.com/NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent).
MIT-licensed. Distinctive features:

- **~25 platform adapters** across `plugins/platforms/` and
  `gateway/platforms/`: Telegram, Discord, Slack, WhatsApp
  (Baileys **and** Meta Cloud API), Signal, iMessage (BlueBubbles),
  Matrix, Teams, Email, SMS, IRC, Line, DingTalk, Feishu,
  WeChat / WeCom / QQBot, Google Chat, Home Assistant, ntfy,
  Microsoft Graph webhook, A2A, and a "raft" / "buzz" / "photon"
  set of experimental adapters. The landing-page count of "5" is
  years out of date; the real surface is far larger.
- **Skills that self-author**: the marquee feature. Portable
  `SKILL.md` format, compatible with the
  [agentskills.io](https://agentskills.io) open standard, browsable
  community hub, ~40+ bundled skills, and — crucially — the agent
  can write its own skills during a run.
- **Seven sandboxing backends**: local terminal, Docker, SSH,
  Singularity, Modal, Daytona, Vercel Sandbox. Serverless
  hibernation means idle-cost approaches zero.
- **Nous Portal** as a bundled monetised LLM upstream (Free /
  Plus $20 / Super $100 / Ultra $200 per month), plus first-party
  support for OpenAI, Anthropic, OpenRouter, Azure Foundry, AWS
  Bedrock, xAI, Vercel AI Gateway, vLLM, and any OpenAI-compatible
  endpoint.
- Sub-agents, MCP client + server, A2A protocol, and a JSONL A2A
  audit log at `~/.hermes/a2a_audit.jsonl`.

**Repository state at time of writing (2026-09-06):** 242k+ stars,
395 contributors (top: `teknium1` at ~14k commits), roughly weekly
releases, active Discord, translated docs in 14 languages. This is
not a hobby project — it is a well-resourced open-source flagship.

## What rousseau-agent is (for contrast)

Single static Go binary. Nine chat transports (WhatsApp, Telegram,
Discord, Signal, iMessage, Matrix, Slack, Email, SMS). Six LLM
providers (Anthropic API, Claude CLI, OpenAI, OpenRouter / Ollama /
llama.cpp via OpenAI-compatible shim, Bedrock, Vertex). Rootless
Podman Quadlet deployment as the reference runtime. SLSA-3
provenance, cosign-signed containers, reproducible builds. MCP
client + server both first-class. Sub-agents, cron scheduler,
cross-session recall, LLM-summarised compression, plan mode,
memory tiers. Dual-licensed core (Apache-2.0 OR MIT) plus a
proprietary Enterprise Edition unlocked at runtime by an
offline-verified Ed25519-signed license key gating SSO, audit
egress to SIEMs, and RBAC + OPA policy — all in the same binary.

## Where each is stronger

| Dimension | Hermes Agent | rousseau-agent |
|---|---|---|
| Language / runtime | Python 3.11 + Node.js sidecar for the WhatsApp bridge | Go, single static binary |
| Install | `curl \| bash` pulls uv / Python / Node / ffmpeg / ripgrep into `~/.hermes/` | Single binary drop-in, or one-file container image |
| Chat transports | ~25 (incl. WhatsApp Baileys AND Meta Cloud API, WeChat, Line, DingTalk, Feishu, Teams, IRC, Home Assistant, and more) | 9 |
| WhatsApp library | Baileys (Node) + optional Meta Cloud API | whatsmeow (Go) |
| WhatsApp ban-risk mitigation | Behavioural advice + Cloud API fallback | Same behavioural advice (Cloud API fallback on roadmap) |
| LLM providers | ~10 (incl. Nous Portal, Azure Foundry, xAI, Vercel AI Gateway) | 6 |
| Skills | agentskills.io compatible, community hub, ~40+ bundled, agent self-authors | Skills package (bundled + operator-authored); progressive-disclosure refactor in flight |
| Sub-agents | Yes | Yes (orchestrator-worker pattern with token budget cap) |
| MCP client / server | Client + optional MCP servers shipped | Client + server, both first-class |
| A2A | Yes, with append-only audit JSONL | Yes, HTTP/SSE per Google A2A spec |
| Sandboxing backends | 7 (local / Docker / SSH / Singularity / Modal / Daytona / Vercel) | Local + container (Podman) |
| Container hardening | Optional Docker adapter | Rootless Podman, drop-all-caps, seccomp, read-only rootfs, tmpfs /tmp, `UserNS=keep-id`, `NoNewPrivileges` |
| Supply-chain provenance | Standard GitHub Actions releases | SLSA-3 provenance, cosign keyless signatures, reproducible-build CI gate, CycloneDX SBOM per release |
| Enforced SSO (OIDC / SAML) | Generic OIDC on the dashboard only; no SAML | **Enterprise Edition:** enforced SSO/OIDC + SAML brokered into every session, license-gated |
| RBAC | None | **Enterprise Edition:** role hierarchy + multi-party approval |
| OPA / Rego policy | None (0 hits for `rego` / `opa` in the tree) | **Enterprise Edition:** OPA policy runtime, per-tool decisions |
| Tamper-evident audit egress | Plain JSONL append files | **Enterprise Edition:** hash-chained audit egress to Splunk HEC / Datadog / OTLP / HTTPS sinks |
| Compliance posture | "No telemetry, MIT" | SOC 2 / GDPR-DPA / HIPAA-ready docs; Enterprise Edition designed for regulated verticals |
| License model | MIT (Hermes Agent) + paid SaaS at the LLM layer (Nous Portal) | Open core + paid Enterprise Edition license, same signed binary |
| Buyer target | Prosumer, developer, ML researcher | Regulated enterprise + sovereignty-first prosumer |
| Community scale | 242k+ stars, 395 contributors, ~weekly releases | Small, growing; solo-maintained today |

## When to pick which

**Pick Hermes Agent when:**

- You want the widest possible messaging surface (Chinese-market
  transports like WeChat / WeCom / Feishu / DingTalk / Line, or IRC /
  Home Assistant / MS Graph webhooks) out of the box.
- You want the Skills self-authoring loop and a mature community
  skill hub with 40+ ready-made skills.
- You are comfortable running a Python + Node stack, and Modal /
  Daytona / Vercel serverless is where you'd rather host.
- You are a prosumer or developer, and you value MIT-simplicity
  over enterprise-governance features you'd never turn on.
- You want a strong open-source community to fall back on when the
  behaviour is unexpected — 395 contributors versus a small
  focused team.

**Pick rousseau-agent when:**

- Your buyer is regulated (banking under DORA, healthcare under
  HIPAA, EU public sector under the AI Act) and SSO / RBAC / OPA
  policy / signed audit egress are non-negotiable — Hermes ships
  none of those first-class.
- Your ops team wants a single static Go binary rather than a
  Python + Node runtime with `uv` / `pnpm` / Node version drift.
- Your procurement team wants SLSA-3 provenance, cosign
  signatures, reproducible builds, and a CycloneDX SBOM per
  release before they even open the RFP.
- Your compliance team wants the same signed binary in production
  as in dev — with the Enterprise features locked behind a runtime
  license check rather than a separate "enterprise build" the
  auditor can't reproduce.
- You explicitly do NOT want a cloud LLM-proxy SaaS in the loop
  and prefer bringing your own provider credentials.
- You are the enterprise-tier buyer that Hermes's "no enterprise
  tier, no SLA" model does not currently serve.

## The honest limitation

Hermes has 242k stars and a research lab behind it. rousseau-agent
has a fraction of that reach today. If awareness / mindshare /
community size is your first-order concern, Hermes wins on
distribution and it isn't close. rousseau's bet is that the
regulated-enterprise buyer cares less about star count and more
about the governance stack, the supply-chain story, and the vendor
relationship — a distribution channel Hermes is not currently
optimising for.

## A note on the WhatsApp ban-risk problem

Both projects use unofficial WhatsApp libraries (Baileys for Hermes,
whatsmeow for rousseau-agent). Both projects carry the "Meta may
restrict / ban the number using an unofficial client" risk. Neither
has fully solved it; both give the same behavioural advice
(dedicated number, no bulk, no unsolicited outbound). Hermes has
one advantage rousseau does not (yet) have: a shipped Meta
Cloud API adapter as an official-path fallback. Adding an
Enterprise-tier "official BSP-brokered WhatsApp" path to
rousseau-agent is on the roadmap, and would close the last
"Hermes has this and we don't" gap on the transport layer.

## References

- [github.com/NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent)
- [hermes-agent.org](https://hermes-agent.org)
- [portal.nousresearch.com](https://portal.nousresearch.com) — Nous Portal pricing
- [agentskills.io](https://agentskills.io) — Skills open standard
- Related rousseau docs: [WHY_NOT_OPENCLAW.md](./WHY_NOT_OPENCLAW.md), [WHY_NOT_TRUSTCLAW.md](./WHY_NOT_TRUSTCLAW.md), [WHY_NOT_ZEROCLAW.md](./WHY_NOT_ZEROCLAW.md), [COMPETITORS.md](./COMPETITORS.md), [COMMERCIAL.md](./COMMERCIAL.md)
