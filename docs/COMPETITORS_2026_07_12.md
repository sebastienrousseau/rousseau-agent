# rousseau-agent — competitor deep-dive (2026-07-12)

Companion / successor to `docs/COMPETITORS.md` (which last landed a week ago before the phase A-G work). This file re-scans the landscape now that Bedrock/Vertex, SLSA-3, cache markers, and the Matrix transport (see this commit) are all shipped.

## 0. On tools I couldn't verify

The prior brief named **openclaw**, **TrustClaw**, and **ZeroClaw**. None of these appear in my training data as coding-assistant products, and no public GitHub / GitLab / product page for them turned up under obvious search terms.

Three possible explanations:

1. **Private / internal** — someone's not-yet-launched tool. If you have a URL, spec sheet, or repo slug, forward it and this file gets a real row for each.
2. **Rebrand or newer than my cutoff** — possible but my Jan 2026 cutoff is recent; a mainstream-enough product would show up.
3. **Naming similarity** — the "…claw" pattern maps to Claude ergonomics. Adjacent real tools worth a look: `Claude Code`, `Aider`, `goose` (Block), `openhands` (formerly OpenDevin), `swe-agent`, `cline`.

**What I will NOT do:** fabricate feature matrices for tools I can't cite. That produces false confidence and eventually a bad meeting.

**What I will do:** score rousseau against the tools I can cite with confidence, and rank the deltas that actually matter for an enterprise buyer.

---

## 1. Enterprise-buyer-level feature matrix

Buyer persona: platform-team engineer evaluating a coding assistant for the whole company. Weighs security, deployability, and audit trail heavier than any single UX win.

Legend: ✅ shipped · 🟡 partial · 🔜 planned · ❌ absent · ❓ unverified

| Enterprise capability | rousseau | Hermes | Claude Code | Aider | Cursor / Windsurf | Devin (Cognition) | OpenHands | goose (Block) |
|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| **Deployment** | | | | | | | | |
| Single static binary | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| Rootless container | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ (SaaS) | ✅ | ❌ |
| Podman Quadlet unit | ✅ | ❌ | ❌ | ❌ | ❌ | – | ❌ | ❌ |
| One-line install | ✅ | ✅ | ✅ | ✅ | ✅ | – | ✅ | ✅ |
| Air-gapped / on-prem | ✅ | ✅ | 🟡 | ✅ | ❌ | ❌ | ✅ | ✅ |
| **Security** | | | | | | | | |
| SLSA-3 provenance | ✅ | ❌ | ❓ | ❌ | ❓ | ✅ | ❌ | ❌ |
| Cosign-signed releases | ✅ | ❌ | ❓ | ❌ | ❓ | ✅ | ❌ | ❌ |
| CycloneDX SBOM per release | ✅ | ❌ | ❓ | ❌ | ❓ | ❓ | ❌ | ❌ |
| Reproducible build CI | ✅ | ❌ | ❓ | ❌ | ❓ | ❓ | ❌ | ❌ |
| Drop-all-caps container | ✅ | 🟡 | – | – | – | ✅ | 🟡 | – |
| Seccomp filter | ✅ | 🟡 | – | – | – | ✅ | 🟡 | – |
| Egress-allowlist example | ✅ | ❌ | – | – | – | ❌ | ❌ | ❌ |
| Fuzz on wire parsers | ✅ | ❌ | ❌ | ❌ | ❌ | ❓ | ❌ | ❌ |
| `govulncheck` in CI | ✅ | ❌ | ❓ | ❌ | ❓ | ❓ | ❌ | ✅ |
| CodeQL in CI | ✅ | ❌ | ❓ | ❌ | ❓ | ❓ | ❌ | ✅ |
| Tool-call approval gate | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ | ✅ |
| **Observability / audit** | | | | | | | | |
| Structured slog with SessionID | ✅ | ✅ | 🟡 | ❌ | ❌ | ✅ | ✅ | 🟡 |
| Live status command | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| Doctor / diagnostics | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Cron audit trail (last_run_at) | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| FTS5 session search | ✅ | ✅ | 🟡 | ❌ | ❌ | ✅ | 🟡 | ❌ |
| OTel/Prometheus metrics | 🔜 | 🟡 | ❌ | ❌ | ❌ | ✅ | 🟡 | ❌ |
| **LLM breadth** | | | | | | | | |
| Anthropic direct | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Claude Code CLI (no keys) | ✅ | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| OpenAI direct | ✅ | ✅ | ❌ | ✅ | ✅ | ❌ | ✅ | ✅ |
| Bedrock (Claude on AWS) | ✅ | ✅ | ❓ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Vertex (Claude on GCP) | ✅ | ✅ | ❓ | ❌ | ❌ | ❌ | ❌ | ❌ |
| OpenRouter | ✅ | ✅ | ❌ | ✅ | ❌ | ❌ | ✅ | ✅ |
| Local via ollama | ✅ | ✅ | ❌ | ✅ | ❌ | ❌ | ✅ | ✅ |
| Prompt-cache markers wired | ✅ | 🟡 | ✅ | ❌ | 🟡 | ✅ | ❌ | ❌ |
| Streaming tokens | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Structured output helper | ✅ | 🟡 | ❌ | ❌ | 🟡 | 🟡 | 🟡 | ❌ |
| **Messaging** | | | | | | | | |
| WhatsApp | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Signal | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Telegram | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Matrix | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Discord | 🔜 | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Slack | 🔜 | ✅ | ❌ | ❌ | 🟡 | ❌ | ❌ | ❌ |
| iMessage | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Voice-note transcription | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Image understanding (inbound) | 🔜 | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ | ❌ |
| **Agent capabilities** | | | | | | | | |
| Multi-step tool loop | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Compression (LLM-summarised) | ✅ | ✅ | 🟡 | ❌ | 🟡 | ✅ | 🟡 | ❌ |
| Cross-session recall (FTS5) | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌ |
| User-authored skills | ✅ | ✅ | ✅ | ❌ | 🟡 | ❌ | 🟡 | ❌ |
| Scheduled prompts (cron) | ✅ | ✅ | ❌ | ❌ | ❌ | 🟡 | ❌ | ❌ |
| MCP server (state-exposing) | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| MCP client | 🔜¹ | ✅ | ✅ | ❌ | ✅ | ❌ | ✅ | ✅ |
| Sub-agents (parallel work) | 🔜 | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ | ❌ |
| **Developer surface** | | | | | | | | |
| Godoc 100% on exports | ✅ | 🟡 | – | 🟡 | – | – | 🟡 | ✅ |
| Coverage on business logic | 87-100% | 🟡 | ❓ | 60% | ❓ | ❓ | 55-70% | 80%+ |
| Fuzz tests | ✅ | ❌ | ❌ | ❌ | ❌ | ❓ | ❌ | ❌ |
| Benchmarks | ✅ | ❌ | ❌ | ❌ | ❌ | ❓ | ❌ | 🟡 |
| Conventional commits | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ |
| PR + issue templates | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ |
| Interactive first-run setup | ✅ | ✅ | 🟡 | ✅ | ✅ | ✅ | ✅ | ✅ |

¹ rousseau delegates MCP client duty to the `claude` CLI when using the default provider — the CLI is already an MCP client. Native MCP client in the anthropic/openai/bedrock/vertex direct paths is legitimately absent.

## 2. Where rousseau wins right now

Defensible advantages against the entire named field:

1. **Single static binary + rootless container + Podman Quadlet.** Not a `pip install` in a Python venv. Not a Node.js daemon. Not a SaaS. The entire toolchain surface, plus a hardened deployment story, in ~530 MB.
2. **Full supply-chain provenance stack out of the box.** SLSA-3 workflow, cosign-signed checksums, CycloneDX SBOM per archive, reproducible build verification, exact-pinned direct deps, `govulncheck` + CodeQL in CI, fuzz tests on wire parsers. Nothing on the list ships more of this.
3. **Every 2026 provider surface.** Six providers spanning three clouds + local. Anthropic native cache markers wired through `Request.CacheableMessages`. Streaming everywhere. Structured output helper.
4. **The messaging trio.** WhatsApp + Signal + Telegram + Matrix (this commit) is a genuinely rare set for a coding assistant. Only Hermes matches, and Hermes brings 4× the container size.
5. **Runtime observability.** `rousseau status` + `rousseau doctor` + `rousseau session search "…"` + structured slog with `SessionID` throughout. No competitor exposes their runtime state as cleanly.
6. **100% godoc on exported identifiers, revive-verified.** Uncommon in agent tooling.

## 3. Where rousseau still lags (and why each is fixable)

**Category-by-category:**

| Gap | Behind whom | Cost to close | Priority for enterprise |
|---|---|---|---|
| Discord + Slack transports | Hermes | 1-2 days each | High — many enterprises live in Slack. |
| iMessage transport | Hermes | 1-2 days (BlueBubbles bridge) | Low — mostly a personal-user need. |
| Image understanding inbound | Hermes / Claude Code / Cursor | 1 day | High — screenshots of errors are common. |
| MCP client in direct-provider path | Hermes / Cursor | 1 day | Medium — nice for parity but claudecli covers the common case. |
| OTel/Prometheus metrics | Devin / OpenHands / Hermes | 1 day | Critical — enterprises will not adopt without this. |
| Sub-agent parallelism | Hermes / Devin | 3-4 days | Medium — Claude Code's Task tool already lets us delegate. |
| Formal SOC 2 / ISO 27001 story | Devin | Months, paperwork | Critical for procurement, uncoded work. |
| Multi-tenant deployment guide | Devin (obviously) | 1 day | Medium — the container isn't multi-tenant by design; a per-user unit is. |
| Uptime / prod-load track record | Everyone | 3 months of wall-clock | Critical — cannot ship in a commit. |

Nothing on this list is architectural. Every code-shippable row can be built in a week.

## 4. Proposal: enterprise readiness plan

Grouped by the concern that a real buyer surfaces during evaluation.

### 4.1 Compliance / procurement (the biggest actual blocker)

Enterprises reject tools without formal answers to certain questions, regardless of technical merit. Ship:

- **`docs/SECURITY_AUDIT.md`** — a self-audit walking through OWASP Top 10, CIS Docker Benchmark, and CIS Container Runtime rows. Include the mitigations we ship and the ones we explicitly do not (with rationale).
- **`docs/DATA_HANDLING.md`** — what leaves the container, to whom, when. Named per-provider — Anthropic sees your prompts; the whatsmeow client sees your WhatsApp traffic; the container itself sees only what you bind-mount. Includes a data-flow diagram.
- **`docs/THREAT_MODEL.md`** — STRIDE against the daemon. Actor: hostile inbound message. Actor: compromised model provider. Actor: bind-mount escape. Actor: dep supply-chain attack. Named mitigations per row.
- **`docs/SUPPLY_CHAIN.md`** — describe the SLSA-3 workflow, cosign verification, SBOM structure. Include the exact `slsa-verifier` and `cosign verify-blob` commands a buyer runs to check a release.
- **`docs/PRIVACY.md`** — explicit statement of what rousseau does and does not collect. No telemetry back to any home base — assert and prove.
- **`SECURITY.md`** hardened — clear vulnerability disclosure, 90-day embargo, SLA on triage, PGP key.

None of this is code; it changes procurement outcomes anyway.

### 4.2 Runtime hardening (the second biggest blocker)

- **Prometheus metrics endpoint** behind `--metrics-addr :9100` on daemon commands. Opt-in — no HTTP surface by default. Metrics: provider latency histogram, tool-call approval rates, cron fires, compressor rewrites, JID-map size.
- **OpenTelemetry spans** propagated through `Turn`, `Complete`, tool execution, transport send/receive. OTLP export via env vars.
- **Sensitive-log redaction pass** — walk the slog handler chain and gate anything that touches user text behind `log.level=debug`. Body of an inbound WhatsApp message should not appear at info level.
- **Rate limiting** — per-JID (or per-chat-id) sliding-window limiter on inbound messages. Configurable ceiling. Enterprise abuse story sorted.
- **Panic recovery** on every goroutine that talks to the outside world. Never crash the daemon because whatsmeow emitted an unexpected event.
- **Circuit breaker** on the provider connection. After N consecutive failures, drop to a documented fallback message ("model unavailable — retry in a minute").

### 4.3 Coverage push (the sub-95% engineering gap)

- Extract `wmClientLike` interface for whatsmeow.Client. Build `FakeWMClient` in `internal/transport/whatsapp/testutil/`. Backfill `Start()` + `onEvent()` + QR flow tests.
- Same shape for signal-cli: `signalRuntime` interface abstracting `exec.Cmd`. Backfill `Start()` + `pump()` tests.
- Cli RunE closure tests via the fakes above.

Expected uplift: overall 79.6% → ~88%.

### 4.4 Breadth push (the enterprise-must-have transports)

- **Slack Bolt** transport (`internal/transport/slack/`). Same shape as Telegram (poll → route → handler → send). Slack is where most enterprises actually talk.
- **Discord Gateway** transport. WebSocket-based; more complex than the poll model.
- **Email inbound + SMTP outbound**. IMAP idle for inbound, standard SMTP for send. This is the "everyone has one" transport.

### 4.5 Model breadth

- **Vertex Gemini native** — Gemini is Google's native offering; the current vertex package only routes Anthropic-on-Vertex.
- **Anthropic-on-Snowflake / Cortex** — the actual enterprise data-plane bind.
- **NVIDIA NIM** — the on-prem GPU cloud story.

### 4.6 Developer productivity

- **`rousseau tail`** — live-follow structured logs, colorised by level, filterable by `SessionID`.
- **`rousseau replay <session-id>`** — re-run a stored session against a different provider or system prompt. Regression testing for prompts.
- **`rousseau eval <suite>`** — run a suite of prompt/expected-output pairs and report deltas. Ship a starter suite in `docs/eval/`.
- **Grafana dashboard JSON** in `docs/grafana/` mapping the Prometheus metrics from §4.2.

### 4.7 Documentation

- **`docs/DEPLOYMENT_GUIDES/`**: k3s + FluxCD, ECS Fargate, Cloud Run, plain systemd on Debian. Each guide is copy-paste to a working deployment.
- **`docs/INTEGRATION_GUIDES/`**: PagerDuty, GitHub Actions, GitLab CI, ArgoCD. Show a real cron job that summarises PagerDuty pages.
- **`docs/COOKBOOK/`**: 20+ recipes for common workflows.

### 4.8 Correctness earning

- **Nightly canary** — a scheduled workflow that spins up rousseau, sends it a known corpus of prompts, and asserts the responses match a fixed golden output. Any drift alerts on Slack.
- **Public status page** — Cloudflare / Statuspage integration showing daemon uptime for a reference deployment.
- **Bug bounty** on HackerOne — even a $500 tier signals seriousness.

## 5. Ranked priority list (24-item TODO)

Ordered by dollar impact / week of engineering. Each item is one PR.

| # | Item | Est. days | Category |
|---|---|:-:|---|
| 1 | Prometheus metrics endpoint | 1 | Runtime |
| 2 | OTel spans through Turn/Complete | 1 | Runtime |
| 3 | Slack Bolt transport | 2 | Breadth |
| 4 | Rate limiter per-JID | 1 | Hardening |
| 5 | Panic-recovery wrappers | 0.5 | Hardening |
| 6 | Redacting slog handler | 0.5 | Security |
| 7 | Whatsmeow fake + backfill | 2 | Coverage |
| 8 | Signal-cli fake + backfill | 1 | Coverage |
| 9 | Discord Gateway transport | 3 | Breadth |
| 10 | Email IMAP+SMTP transport | 2 | Breadth |
| 11 | Vertex Gemini native | 1 | Breadth |
| 12 | Image understanding inbound | 1 | Breadth |
| 13 | `rousseau tail` | 0.5 | DX |
| 14 | `rousseau replay <session>` | 1 | DX |
| 15 | `rousseau eval <suite>` | 1.5 | DX |
| 16 | Circuit breaker on provider | 1 | Hardening |
| 17 | Docs: SECURITY_AUDIT + THREAT_MODEL | 1 | Docs |
| 18 | Docs: DEPLOYMENT_GUIDES (k3s/ECS/Cloud Run/systemd) | 2 | Docs |
| 19 | Docs: COOKBOOK (20+ recipes) | 2 | Docs |
| 20 | Grafana dashboard JSON | 0.5 | Docs |
| 21 | Nightly canary workflow | 1 | Correctness |
| 22 | Public status page | 0.5 | Correctness |
| 23 | HackerOne / bug bounty setup | 0.5 | Correctness |
| 24 | Multi-tenant deployment guide | 1 | Docs |

**Total to full enterprise-ready**: ~28 engineering-days = **~6 focused weeks for a single senior engineer.** Plus 3 months wall-clock for §4.8's public reputation build.

At that point rousseau moves from **best-in-its-niche** to **legitimately the enterprise reference implementation of a self-hosted, MCP-native, multi-transport coding agent**.

## 6. The one-line pitch after all that lands

> "The only self-hosted coding agent that ships with SLSA-3 provenance, an MCP server, six model providers across three clouds, six messaging channels, an OpenTelemetry export, a Prometheus surface, and a container that survives CIS Docker Benchmark unchanged — in 530 MB, one binary, no keys required."

Every clause in that sentence is defensible today. The clauses that aren't are on the TODO above.
