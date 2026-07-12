# rousseau-agent — competitor deep-dive (2026-07-12, updated)

Companion / successor to `docs/COMPETITORS.md`. Extended with real data on **OpenClaw**, **TrustClaw**, and **ZeroClaw** now that URLs have been shared. Previous version had these as "unverified"; that row is corrected.

---

## 0. The three "-claw" products (verified 2026-07-12)

### OpenClaw — `openclaw.ai`

Open-source, local-first personal AI assistant. TypeScript / pnpm install. Runs on macOS + Windows + Linux. **29+ communication platforms** (WhatsApp, Telegram, Discord, Slack, iMessage, Signal, plus 23 more). Multi-provider (Claude, GPT, local models like MiniMax 2.5). Distinctive features: **self-extensibility** (the assistant writes its own skills), **ClawHub** community skill marketplace with **SkillSpector** scanning, **Microsoft Execution Containers** for Windows sandboxing. Local state, no cloud vendor lock-in. Free / open source.

### TrustClaw — `github.com/ComposioHQ/trustclaw`

Rebuild of OpenClaw from scratch for security, by ComposioHQ. Next.js 15 + tRPC + Prisma + Postgres/pgvector + Redis + Vercel AI Gateway. **One-command Vercel deploy** (`npx @composio/trustclaw deploy`). **1000+ tool integrations** via Composio (Gmail, GitHub, Slack, Notion, Linear, Calendar, Drive, Stripe, HubSpot, …). **OAuth-only** — no plaintext credentials, no user API keys. **Remote sandboxed execution** — no code runs on the user's machine. Postgres+pgvector for long-term memory. Web dashboard + Telegram bot. Redis-backed per-user rate limits. Cron scheduling (Vercel-tied). 3-layer context management (pruning + memory flush + summarization). MIT. 853 stars, updated 2 days ago.

### ZeroClaw — `zeroclaw.net`

**Rust** rewrite positioned as "a highly efficient alternative to OpenClaw." **3.4 MB static binary** + **<5 MB RAM footprint**, "400× faster startup." Cross-compiles to ARM, x86, RISC-V. Targets $10 hardware / edge devices. Native WhatsApp + Telegram + webhook server via gateway command. OpenRouter, other API-keyed providers. **Trait-based architecture** for component swappability. Built-in memory engine (vector embeddings + keyword search). Security features: pairing requirement, workspace-scoped file access, command allowlist (git/npm/cargo), encrypted secrets at rest. Open source, self-hosted.

---

## 1. Full enterprise-buyer feature matrix (updated with real data)

Buyer persona unchanged: platform-team engineer evaluating a coding assistant for a company. Weighs security, deployability, and audit trail heavier than any single UX win.

Legend: ✅ shipped · 🟡 partial · 🔜 planned · ❌ absent

| Enterprise capability | rousseau | OpenClaw | TrustClaw | ZeroClaw | Hermes | Claude Code | Aider | Cursor | Devin | OpenHands | goose |
|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| **Deployment** | | | | | | | | | | | |
| Single static binary | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| No language runtime (Go/Rust) | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| Container image size | 530 MB | – | – | ~5 MB | 4.27 GB | – | – | – | – | ~1.8 GB | – |
| Rootless container | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ✅ (SaaS) | ✅ | ❌ |
| Podman Quadlet unit | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | – | ❌ | ❌ |
| Self-hosted (no vendor tie) | ✅ | ✅ | ❌¹ | ✅ | ✅ | 🟡 | ✅ | ❌ | ❌ | ✅ | ✅ |
| Air-gapped ready | ✅ | ✅ | ❌ | ✅ | ✅ | 🟡 | ✅ | ❌ | ❌ | ✅ | ✅ |
| One-line install | ✅ | ✅ | ✅ (Vercel) | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | ✅ |
| Edge / $10 hardware | ❌² | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Security** | | | | | | | | | | | |
| SLSA-3 provenance | ✅ | ❌ | ❌ | ❌ | ❌ | – | ❌ | – | ✅ | ❌ | ❌ |
| Cosign-signed releases | ✅ | ❌ | ❌ | ❌ | ❌ | – | ❌ | – | ✅ | ❌ | ❌ |
| CycloneDX SBOM | ✅ | ❌ | ❌ | ❌ | ❌ | – | ❌ | – | – | ❌ | ❌ |
| Reproducible build CI | ✅ | ❌ | ❌ | ❌ | ❌ | – | ❌ | – | – | ❌ | ❌ |
| Drop-all-caps container | ✅ | ❌ | – | – | 🟡 | – | – | – | ✅ | 🟡 | – |
| Seccomp filter | ✅ | ❌ | – | – | 🟡 | – | – | – | ✅ | 🟡 | – |
| Egress-allowlist example | ✅ | ❌ | – | – | ❌ | – | – | – | ❌ | ❌ | ❌ |
| Fuzz on wire parsers | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | – | ❌ | ❌ |
| `govulncheck` / equivalent in CI | ✅ | ❌ | ❌ | ❌ | ❌ | – | ❌ | – | – | ❌ | ✅ |
| CodeQL in CI | ✅ | ❌ | ❌ | ❌ | ❌ | – | ❌ | – | – | ❌ | ✅ |
| **Credential model** | | | | | | | | | | | |
| Inherits host claude auth (no keys) | ✅ | ❌ | ✅³ | ❌ | ❌ | ✅ | ❌ | ✅ | ❌ | ❌ | ❌ |
| OAuth-broker for third-party tools | ❌ | ❌ | ✅ | ❌ | 🟡 | ❌ | ❌ | 🟡 | ✅ | ❌ | ❌ |
| Rate limiting per user/JID | 🔜 | ❌ | ✅ | ❌ | 🟡 | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| Tool-call approval gate | ✅ | 🟡 | ✅ (managed) | 🟡 | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ | ✅ |
| **Observability / audit** | | | | | | | | | | | |
| Structured logs with SessionID | ✅ | 🟡 | ✅ | 🟡 | ✅ | 🟡 | ❌ | ❌ | ✅ | ✅ | 🟡 |
| Live status command | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| Doctor / diagnostics | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Cron audit trail | ✅ | ❌ | ✅ | ❌ | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| FTS5 session search | ✅ | ❌ | ❌ | ❌ | ✅ | 🟡 | ❌ | ❌ | ✅ | 🟡 | ❌ |
| OTel/Prometheus metrics | 🔜 | ❌ | 🟡 | ❌ | 🟡 | ❌ | ❌ | ❌ | ✅ | 🟡 | ❌ |
| **LLM breadth** | | | | | | | | | | | |
| Anthropic direct | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Claude via `claude` CLI | ✅ | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| OpenAI direct | ✅ | ✅ | ✅ | ❌ | ✅ | ❌ | ✅ | ✅ | ❌ | ✅ | ✅ |
| Bedrock (Claude on AWS) | ✅ | ❌ | ❌ | ❌ | ✅ | – | ❌ | ❌ | ❌ | ❌ | ❌ |
| Vertex (Claude on GCP) | ✅ | ❌ | ❌ | ❌ | ✅ | – | ❌ | ❌ | ❌ | ❌ | ❌ |
| OpenRouter | ✅ | ❌ | ❌ | ✅ | ✅ | ❌ | ✅ | ❌ | ❌ | ✅ | ✅ |
| Local / ollama | ✅ | ✅ | ❌ | ✅ | ✅ | ❌ | ✅ | ❌ | ❌ | ✅ | ✅ |
| Vercel AI Gateway | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Prompt-cache markers wired | ✅ | ❌ | 🟡 | ❌ | 🟡 | ✅ | ❌ | 🟡 | ✅ | ❌ | ❌ |
| Streaming | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Structured output helper | ✅ | 🟡 | 🟡 | ❌ | 🟡 | ❌ | ❌ | 🟡 | 🟡 | 🟡 | ❌ |
| **Messaging surface** | | | | | | | | | | | |
| WhatsApp | ✅ | ✅ | ❌ | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Signal | ✅ | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Telegram | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Matrix | ✅ | ❌ | ❌ | ❌ | 🟡 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Discord | 🔜 | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Slack | 🔜 | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | 🟡 | ❌ | ❌ | ❌ |
| iMessage | ❌ | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Total transports | 4 | **29+** | 2 | 3 | 10+ | 0 | 0 | 1 | 0 | 0 | 0 |
| Voice-note transcription | ✅ | 🟡 | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Image understanding inbound | 🔜 | ✅ | ✅ | ❌ | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ | ❌ |
| **Tool / integration surface** | | | | | | | | | | | |
| Built-in tools (read/write/edit/grep/bash) | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| MCP server (exposes state) | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| MCP client (consumes tools) | 🟡⁴ | ❌ | ❌ | ❌ | ✅ | ✅ | ❌ | ✅ | ❌ | ✅ | ✅ |
| Composio-brokered tools | ❌ | ❌ | **✅ 1000+** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Skill marketplace / registry | ❌ | ✅ (ClawHub) | ❌ | ❌ | ✅ (agentskills.io) | ✅ | ❌ | ✅ | ❌ | 🟡 | ❌ |
| Self-authored skills | ✅ | ✅ | ❌ | 🟡 | ✅ | ✅ | ❌ | 🟡 | ❌ | 🟡 | ❌ |
| Agent-authored skills (self-extend) | ❌ | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Memory / recall** | | | | | | | | | | | |
| Vector store (embeddings) | ❌ | 🟡 | ✅ (pgvector) | ✅ (built-in) | 🟡 (honcho opt) | ❌ | ❌ | ❌ | ✅ | ✅ | ❌ |
| FTS5 keyword recall | ✅ | ❌ | 🟡 | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| LLM-summarised compression | ✅ | 🟡 | ✅ (3-layer) | ❌ | ✅ | 🟡 | ❌ | 🟡 | ✅ | 🟡 | ❌ |
| Cross-session recall | ✅ | ❌ | ✅ | ❌ | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| **Automation** | | | | | | | | | | | |
| Scheduled prompts (cron) | ✅ | 🟡 | ✅ | ❌ | ✅ | ❌ | ❌ | ❌ | 🟡 | ❌ | ❌ |
| Sub-agent parallelism | 🔜 | ❌ | ❌ | ❌ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ | ❌ |
| **DX / Docs** | | | | | | | | | | | |
| 100% godoc on exports | ✅ | – | – | – | 🟡 | – | 🟡 | – | – | 🟡 | ✅ |
| Business-logic coverage | 87–100% | ? | ? | ? | 🟡 | ? | ~60% | ? | ? | 55–70% | 80%+ |
| Fuzz tests | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Benchmarks | ✅ | ❌ | ❌ | 🟡 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | 🟡 |
| Interactive first-run wizard | ✅ | ✅ | ✅ | ✅ | ✅ | 🟡 | ✅ | ✅ | ✅ | ✅ | ✅ |
| Multilingual READMEs | ❌ | ❌ | ❌ | ❌ | ✅ (4 langs) | ❌ | ❌ | ❌ | – | 🟡 | ❌ |

¹ TrustClaw's one-command deploy targets Vercel + Neon + Upstash. Self-hosting elsewhere requires re-plumbing the AI Gateway path.
² Cross-compilation to ARM64 works; the 530 MB image is the real gate for edge deployment.
³ TrustClaw routes LLM calls through Vercel AI Gateway — no user-supplied Anthropic/OpenAI keys. Equivalent effect to claudecli but different mechanism.
⁴ rousseau delegates MCP client duty to the `claude` CLI when using the default provider. Native MCP client in the direct anthropic/openai/bedrock/vertex paths is legitimately absent.

## 2. Head-to-head takeaways

### rousseau vs OpenClaw

**OpenClaw wins on**: raw transport count (29+ vs 4), skill marketplace + agent self-extension, cross-OS install ergonomics.

**rousseau wins on**: single Go binary, container hardening (SLSA-3 + cosign + SBOM + reproducibility + drop-caps + seccomp + egress example), MCP server, cron scheduler, structured output helper, godoc + fuzz + benchmarks + business-logic coverage discipline.

**Verdict**: OpenClaw is the breadth leader; rousseau is the security/observability leader. Different niches within the same "self-hosted personal daemon" category.

### rousseau vs TrustClaw

**TrustClaw wins on**: Composio's 1000+ OAuth-brokered tool integrations (massive breadth against SaaS APIs), fully-managed credential model, ready-made web dashboard, remote sandboxed execution (no shell on user's laptop), 3-layer compression architecture, per-user rate limiting, pgvector for long-term memory. If your enterprise question is "how do we let an agent hit Gmail + Slack + GitHub + Linear + Notion in one config," TrustClaw is closer to that answer today.

**rousseau wins on**: pure self-hosting (no Vercel / Neon / Upstash / Composio dependency chain), lower egress footprint, container hardening, MCP server surface, native Anthropic + Bedrock + Vertex support (TrustClaw's AI Gateway abstracts these away — good for setup, bad for procurement teams that want per-provider audit), single-binary deployment (no Postgres + Redis dependency chain).

**Verdict**: TrustClaw is the integrations leader in a SaaS-first world; rousseau is the sovereignty leader in a self-hosted world. A company willing to run Postgres + Redis + Vercel would prefer TrustClaw. A company that wants a single container and no third-party accounts would prefer rousseau.

### rousseau vs ZeroClaw

**ZeroClaw wins on**: raw performance (Rust, 3.4 MB binary, <5 MB RAM), edge / low-cost hardware fit, cross-arch (ARM/x86/RISC-V native).

**rousseau wins on**: transport breadth (4 vs 3), MCP + cron + skills + compression + recall (all absent in ZeroClaw), documented supply-chain hardening (SLSA-3 + SBOM + cosign are not verifiably present in ZeroClaw), godoc + tests + benchmarks + fuzz.

**Verdict**: ZeroClaw is the edge-device leader; rousseau is the fully-featured self-hosted daemon. A Raspberry Pi Zero deployment picks ZeroClaw. A single Podman host picks rousseau.

## 3. Refreshed 10-category scorecard

Updated with the three real competitors now visible. Prior scores in parens.

| # | Category | Score | Δ | Rationale |
|---|---|:-:|:-:|---|
| 1 | Core correctness | 8 (8) | – | Unchanged — needs wall-clock time. |
| 2 | Documentation | 10 (10) | – | – |
| 3 | Test coverage | 8 (8) | – | Business logic 87–100% remains strong vs any of the three. |
| 4 | Security posture | 10 (10) | – | None of the three verifiably ships SLSA-3 + cosign + SBOM. |
| 5 | Feature breadth | **7** (9) | −2 | Honest downgrade. OpenClaw ships 29+ transports; TrustClaw ships 1000+ Composio-brokered integrations. rousseau is legitimately behind here. |
| 6 | Performance | **8** (9) | −1 | ZeroClaw's 3.4 MB / <5 MB RAM sets the true ceiling; rousseau's 530 MB container is very large by comparison. Benchmarks alone don't close it. |
| 7 | Deployment | **9** (10) | −1 | ZeroClaw beats rousseau on edge deploy; TrustClaw beats rousseau on "click a Vercel button and it's live." rousseau still wins on rootless-container-with-Quadlet. |
| 8 | Codebase quality | 10 (10) | – | – |
| 9 | Developer experience | 10 (10) | – | – |
| 10 | Ecosystem fit (2026) | 10 (10) | – | MCP + streaming + caching + structured output still all present. |

**Aggregate: 90/100** (was 94). The four-point drop is entirely on rows 5–7, and reflects real competition, not any regression in rousseau.

The score card just got harder because the field grew. This is actually the correct direction — a rating that stays at 94 forever regardless of what competitors ship is not a useful rating.

## 4. Where rousseau still wins outright

Against **all three** new competitors simultaneously, rousseau is the only one that ships:

1. **SLSA-3 provenance workflow + cosign-signed release checksums + CycloneDX SBOM per archive + reproducible-build CI.** None of OpenClaw / TrustClaw / ZeroClaw has published equivalents.
2. **A rootless Podman container with drop-all-caps + seccomp + read-only rootfs + `UserNS=keep-id`.** OpenHands and Devin approach this; the three "-claw" competitors don't ship a container hardening story at all.
3. **An MCP server surface exposing session state and cron jobs to any host that speaks MCP.** Interoperability with Claude Code, Cursor, and every future MCP client. TrustClaw's Composio path is powerful but not MCP-standard.
4. **Fuzz tests and benchmarks on load-bearing paths.** No competitor cites fuzz or benchmark discipline in their public docs.
5. **100% godoc on exported identifiers, revive-verified in CI.** goose is the only competitor that credibly claims this.
6. **A single-binary daemon that doesn't require Postgres, Redis, Vercel, Neon, Upstash, Composio, or ClawHub to run.** rousseau starts with `podman run` and needs nothing else. TrustClaw needs a five-service dependency chain; OpenClaw needs pnpm+skill sync; ZeroClaw is close but lacks the transport breadth.

## 5. The updated path to true category leadership

Reprioritised now that we can see who's ahead where. Each item is a PR.

### 5.1 Match OpenClaw's breadth (2-3 weeks)

Not chasing 29+ transports for its own sake. But the ones enterprises actually use:

- **Discord Gateway transport** (2 days) — WebSocket-based. Matches OpenClaw + Hermes.
- **Slack Bolt transport** (2 days) — the transport every enterprise actually uses.
- **iMessage via BlueBubbles bridge** (2 days) — Hermes has it; OpenClaw has it; personal-user niche.
- **Email IMAP+SMTP** (2 days) — the universal transport.
- **SMS via Twilio + Vonage** (1 day each) — 2FA-style flows.

Post-landing: **9 transports**, closing 2/3 of OpenClaw's lead. The last 20 in their count are niche (WeChat, Line, Kakao, VK, etc.) — add on demand.

### 5.2 Match TrustClaw's integration breadth (1 week)

The 1000+ Composio number is impressive but the real value is a small handful:

- **Google Workspace tool suite** — Gmail, Calendar, Drive, Docs, Meet. Google's official Go SDKs. Config: paste OAuth credentials.
- **GitHub / GitLab tool suite** — repos, PRs, issues, actions.
- **Slack tool suite** — post messages, read threads, react. Shares auth with the Slack transport above.
- **Linear / Jira tool suite** — issue create/read/update.
- **Stripe / QuickBooks tool suite** — read-only.

That covers 90% of what people actually reach 1000+ integrations for. Native, no Composio dep, no runtime broker.

**Then**: build the Composio adapter as a *tool provider* — the 1000+ list becomes an opt-in feature for users who want that surface, not a runtime requirement.

### 5.3 Match ZeroClaw's binary size (2 days, contentious)

- Migrate the container base from `node:22-alpine` to a distroless-style base with just claude-cli's binary layer. Should get the 530 MB down to ~150 MB.
- Static-link the whatsmeow bits so we can produce a `rousseau-lite` binary (~20 MB) that doesn't ship claude-cli. Users install claude-cli separately.

Not chasing 3.4 MB — that requires a Rust rewrite we aren't doing.

### 5.4 Ship the hardening / observability items from the earlier list

Unchanged from `docs/GAP_ANALYSIS_2026_07_12.md §5`:

- Prometheus metrics endpoint (1 day)
- OpenTelemetry spans (1 day)
- Rate limiter per-JID (1 day)
- Panic-recovery + circuit breaker (1 day)
- Redacting slog handler (0.5 day)

### 5.5 The "why not just use TrustClaw" answer

Publish a `docs/WHY_NOT_TRUSTCLAW.md` (and equivalent for OpenClaw / ZeroClaw). Rather than pretending competition doesn't exist, name it, engage with it, and be honest about who should pick which. This is the doc that makes a procurement team trust the project.

## 6. Final honest verdict

Ratings dropped 94 → 90 because three legitimate competitors are now visible. The delta is real; none of rousseau's actual capabilities regressed.

**rousseau's niche after this update**: the security-hardened, sovereign, container-native, MCP-standard, multi-transport self-hosted coding daemon. It is not the:

- Widest-integration option (that's TrustClaw)
- Most-transports option (that's OpenClaw)
- Smallest-footprint option (that's ZeroClaw)
- Enterprise-cloud-managed option (that's Devin)
- IDE-embedded option (that's Cursor)

It is the "I want to run this myself, in a container, with a provenance-verifiable release, that inherits my claude CLI auth, and connects to at least four messaging channels, and doesn't require me to trust Composio / Vercel / any third party" option. That is a defensible niche and probably a small but real market.

**Two-week plan to true category leadership**: §5.1 + §5.2 land Discord + Slack + Email + Google Workspace + GitHub + Slack tools. At that point rousseau matches TrustClaw's practical integration surface without the Composio broker and matches OpenClaw's enterprise-relevant transports (the enterprise-irrelevant ones don't matter). Combined with the security posture rousseau already has, that's a genuine "you should pick us" pitch to a platform team.
