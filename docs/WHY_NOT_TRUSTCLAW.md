# Why not just use TrustClaw?

**TL;DR:** TrustClaw is the right answer if your organisation is happy
running on Vercel + Neon + Upstash + Composio, wants OAuth-brokered
access to 1000+ SaaS APIs out of the box, and doesn't need any
messaging surface beyond a web dashboard and a Telegram bot.
rousseau-agent is the right answer if your organisation wants a single
container, provenance-verifiable releases, per-provider LLM
audit trails, no third-party service dependencies, and eight extra
messaging transports.

They occupy different niches within "self-hosted personal AI daemon,"
and this document lays out where each is stronger so you can make the
call quickly.

## What TrustClaw is

[TrustClaw](https://github.com/ComposioHQ/trustclaw) is a rebuild of
OpenClaw by ComposioHQ (the team that runs the Composio tool broker).
Under the hood it's a Next.js 15 application backed by Postgres +
pgvector for long-term memory, Redis for per-user rate limits, and the
Vercel AI Gateway for LLM routing. Tool calls are brokered through
Composio's OAuth flow, giving the model access to 1000+ third-party
SaaS APIs (Gmail, Slack, GitHub, Notion, Linear, Calendar, Drive,
Stripe, HubSpot, and many others) without the user ever supplying an
API key. The whole stack deploys with one command:
`npx @composio/trustclaw deploy`.

**Repository state at time of writing (2026-07-12):** ~853 stars,
active weekly commits, MIT license.

## Where TrustClaw wins

**1000+ integrations vs rousseau's 26.** rousseau ships native
adapters for the SaaS surfaces enterprise operators actually reach for
(GitHub, Slack, Gmail, Calendar, Drive, Linear, Stripe) but TrustClaw's
Composio pipeline unlocks a much larger long tail — Notion, HubSpot,
Salesforce, ClickUp, Airtable, Discord (as a tool, not a transport),
Zoom, PagerDuty, and hundreds more. If you need one of the specific
1000+ that rousseau doesn't ship natively, TrustClaw is closer to your
answer today.

**Fully-managed OAuth.** Composio handles the OAuth consent screen,
token refresh, and revocation for every provider. rousseau ships an
encrypted token vault and OAuth broker (§2 of the implementation
plan) but the operator supplies the client id / secret per provider
and grants each scope manually. That's honest work for an
operator-run daemon; it's not what a marketing team wants.

**Managed deployment.** `npx @composio/trustclaw deploy` provisions
the Vercel project, the Neon Postgres, the Upstash Redis, and the
Composio account. rousseau needs `podman run` and a config file. Both
are fast, but TrustClaw hides more of the plumbing.

**Web dashboard.** TrustClaw ships a Next.js dashboard for chat
history browsing, per-tool audit logs, and user management. rousseau
exposes the same data via `rousseau session ls / show / search` and a
Bubble Tea TUI. If you want a browser tab, TrustClaw wins.

**pgvector for long-term semantic memory** is production-ready today;
rousseau's equivalent is on the roadmap (§9 of the implementation
plan) via sqlite-vec.

**3-layer context management** (pruning, memory flush, summarisation).
rousseau ships a comparable LLM-summarised compressor but its layering
is single-pass.

## Where rousseau wins

**No third-party service chain.** rousseau starts with `podman run
rousseau-agent` and needs nothing else — no Postgres, no Redis, no
Vercel, no Neon, no Upstash, no Composio account, no ClawHub. Every
piece of state lives in a single SQLite file on disk. Every network
call goes directly to the LLM provider or the tool endpoint you
configured. For teams whose procurement pipeline chokes on "another
SaaS to audit," this is the load-bearing difference.

**Provenance-verifiable releases.** Every rousseau release ships
with:

- SLSA-3 provenance signed by GitHub's OIDC identity
- Cosign-signed archive checksums
- CycloneDX SBOM per architecture
- Reproducible-build CI gate that fails a release whose bytes don't
  match a fresh rebuild

TrustClaw ships neither an SBOM nor a signed release attestation
today. For anyone whose security review includes SLSA compliance,
this is not a philosophical preference — it's a check-list.

**Direct provider paths.** rousseau's LLM adapter layer speaks to
Anthropic direct, Bedrock, Vertex, OpenAI, OpenRouter, Ollama, and the
Claude CLI. Each provider is a separate audit surface — you can pin
Bedrock alone, or route enterprise traffic to Vertex while dev traffic
hits Anthropic direct. TrustClaw routes every LLM call through the
Vercel AI Gateway, which is convenient for setup but hides the
per-provider path from your finance and procurement teams.

**MCP server surface.** rousseau exposes its own state (sessions,
cron jobs, allowlists) as an MCP server. Any host that speaks MCP —
Claude Code, Cursor, custom scripts — can read from and act on
rousseau's data without a Composio-shaped broker in the middle.

**Container hardening.** rousseau ships a rootless Podman container
with drop-all-capabilities, seccomp, read-only rootfs, `UserNS=keep-id`,
and a documented egress-allowlist example. TrustClaw's runtime target
is Vercel Functions; the hardening story is Vercel's, not the
project's.

**Nine chat transports.** WhatsApp, Signal, Telegram, Matrix, Slack,
Discord, SMS (Twilio/Vonage), iMessage (BlueBubbles), Email
(IMAP+SMTP). TrustClaw ships web + Telegram. If you want to say
"reply to this in WhatsApp / Slack DM / Signal," rousseau is the
answer.

**Fuzz + property tests on every transport parser.** Every wire
parser has both a Go native fuzz target (`go test -fuzz`) and a
`testing/quick` property test. TrustClaw's public repo doesn't cite
fuzz or property tests on its adapter code.

**Voice-note transcription** ships out of the box on WhatsApp via
whisper.cpp; not a TrustClaw capability.

## The scenario, laid out on both

**Job:** wire an agent that (1) triages Gmail inbound, (2) posts a
summary to a Slack channel, (3) files GitHub issues for anything the
model classifies as a bug, (4) reachable from your phone via WhatsApp.

**TrustClaw path**

1. `npx @composio/trustclaw deploy` → Vercel project, Neon DB, Upstash
   Redis, Composio account.
2. Composio OAuth flows: Gmail, Slack, GitHub.
3. Web dashboard: configure the "triage inbound mail" prompt.
4. `??` — no first-party WhatsApp transport. Either bolt on a
   webhook-shaped WhatsApp integration through Composio (the model
   posts to a webhook that fans out to WhatsApp) or accept web +
   Telegram only.

**rousseau path**

1. `podman run` the container against a config file with your Anthropic
   key, GitHub PAT, Slack bot token, WhatsApp session pin, and Gmail
   OAuth client id/secret.
2. `rousseau auth google` — one-time OAuth flow through the local
   127.0.0.1:8765 callback.
3. `rousseau whatsapp --allow <your-jid>` — QR pair.
4. Config already registered `gmail_list`, `slack_post_message`, and
   `github_create_issue` from the integrations config; the model
   invokes them on the triage prompt.

Both work. TrustClaw is fewer steps up front but relies on four
external services being healthy. rousseau is a container + a config
file and stays reachable while your entire dev VPC is offline.

## Score-card view

Reproduced from `docs/COMPETITORS_2026_07_12.md`, updated to reflect
Week-1 (OAuth broker + hardening) and Week-2 (26 native tool
integrations) work:

| Axis | rousseau | TrustClaw |
| --- | :-: | :-: |
| Native tool count | 26 | 1000+ (Composio) |
| Provenance / SBOM / cosign | ✅ | ❌ |
| Reproducible build in CI | ✅ | ❌ |
| Rootless hardened container | ✅ | ❌ |
| Direct provider audit | ✅ | ❌ (Gateway) |
| MCP server surface | ✅ | ❌ |
| Chat transports | 9 | 2 (web, telegram) |
| Voice-note transcription | ✅ | ❌ |
| pgvector-shaped recall | 🔜 (§9) | ✅ |
| Web dashboard | ❌ (TUI) | ✅ |
| Managed OAuth (Composio) | ❌ (own broker) | ✅ |
| Runtime dep chain | 0 external | Vercel+Neon+Upstash+Composio |

## Who should pick which

**Pick TrustClaw if…**

- You want a browser tab as the primary UI.
- The 1000+ Composio surface is more valuable to you than SLSA + SBOM
  + reproducible build.
- Vercel + Neon + Upstash + Composio are already inside your compliance
  boundary.
- Web + Telegram are enough for your users.

**Pick rousseau if…**

- Your security review expects SLSA-3, SBOM, cosign, and a reproducible
  build.
- You want zero third-party service dependencies — one container, one
  SQLite file, done.
- Your users want to reach the agent from WhatsApp / Signal / Slack /
  Discord / Matrix / Email / SMS / iMessage — not just a web tab.
- You want your per-provider LLM spend visible on the provider's own
  billing dashboard, not aggregated inside a Gateway.
- You want to run the agent on a laptop, a home server, an air-gapped
  Podman host, or a Kubernetes cluster with equal ease.

## Direct interop

The two projects can coexist. rousseau's OAuth broker (§2) and the
Composio adapter path (§5.2 of the competitor doc) are on the roadmap
so a rousseau operator who wants Composio's 1000+ surface can register
it as a single tool provider without moving the entire stack.

We recommend that path — the "why not both" answer — for teams that
value rousseau's deployment properties but need the long-tail
integration surface. Ship date TBD; watch `docs/ROADMAP.md`.
