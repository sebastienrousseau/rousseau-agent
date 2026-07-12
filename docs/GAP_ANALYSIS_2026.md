# rousseau-agent — gap analysis and forward plan (July 2026)

_Last touched: 2026-07-12. Supersedes the ROADMAP §2/§3 sections; ROADMAP.md remains as the shipped-record archive._

Context: `rousseau-agent` has now shipped every Q3/Q4 2026 roadmap item plus a set of hardening work in the last week. This doc is an honest re-scan — where the project sits, what 2026 trends actually change the target, which categories genuinely could still improve, and a phased plan to close the ones worth closing.

## 1. Where rousseau sits today (2026-07-12)

**Shipped since the initial July 11 rating:**

- Full transport suite: WhatsApp (whatsmeow), Signal (signal-cli JSON-RPC), Telegram (bot API long-poll)
- Provider suite: `claudecli`, `anthropic` (with streaming), `openai` / `openrouter` / `ollama` (via BaseURL)
- Streaming everywhere: `agent.StreamingProvider` implemented by both providers; TUI renders token-by-token
- Approval gate (AllowAll / DenyAll / regex-Pattern) consulted before every tool call
- Session compression (LLMCompressor, `[rousseau-compressed]` marker) + cross-session recall (FTSRecall against SQLite FTS5)
- Skills loader (agentskills.io-style, Markdown + YAML front-matter)
- MCP server (four read-only tools; live-verified against real session data)
- Cron scheduler goroutine actually firing scheduled jobs, delivering via any transport
- Podman Quadlet unit with rootless + drop-all-caps + read-only rootfs + `UserNS=keep-id` + seccomp
- SLSA-3 provenance workflow + cosign-signed checksums + CycloneDX SBOM per release
- Benchmarks (`make bench`) — `ResolveInbound` at 121 ns/op / 64 B / 3 allocs
- Fuzz tests (`make fuzz`) — `FuzzResolveInbound` and `FuzzServe` (MCP) both clean; already caught two real bugs
- `rousseau status` for runtime observability
- 100% godoc on exported identifiers
- 79.5% overall coverage on internal packages (business logic 87–100%)
- Live deployed as a rootless container via systemd Quadlet; connected to WhatsApp; cron ticking

**Numbers as of `4d89cc1`:**
- ~9,500 lines of Go across 15 internal packages
- 20 dependencies (all exact-pinned)
- 530 MB container image (vs Hermes's 4.27 GB)
- ~40 automated tests per hot package
- Zero known bugs (that we haven't already fixed)

## 2. The 2026 trends actually worth adjusting for

Refresh of what the market wants that we don't already have. Every trend below is one we could act on; some we absolutely should not.

### 2.1 Trends worth reacting to

- **Agentic browser is now standard**, thanks to Chrome DevTools MCP + Anthropic Computer Use graduating out of beta. Coding assistants are increasingly expected to open GitHub PRs, click through Grafana dashboards, and screenshot layout bugs. We deliberately delegate this to Claude (which has browser tools built in), but users don't always speak to us via Claude — Bedrock / Vertex users are stuck.
- **Structured output / JSON Schema constraints** as first-class. OpenAI's `response_format: json_schema`, Anthropic's tool-use as structured output, Vertex's constrained generation. rousseau exposes tool-use but not schema-constrained free text — users who want typed replies must post-process.
- **Prompt caching is now default**. Anthropic's ephemeral cache, OpenAI's automatic cache. Rousseau uses `claudecli` (free ride) but the direct `anthropic` provider doesn't set cache markers explicitly — we're leaving money on the table for anyone using that path.
- **Local models jumped again in Q2 2026**. Qwen3-Coder-72B and Codestral-Reasoning-24B now beat mid-2025 GPT-4 on real coding tasks. rousseau supports ollama via the OpenAI shim — the question is whether we cost-estimate per turn so users can pick tier.
- **Regulatory disclosure enforcement** started biting in the EU in June 2026 (Article 50 penalties for undisclosed AI content). Our `💎 *Rousseau Agent*` header covers this. Extend to Signal/Telegram which currently ship without it as strongly (they only prepend if configured).
- **Sandboxing is now a hiring signal**. YC-batch security engineers explicitly check "does this container drop all caps by default." We do — this is a marketing-worthy fact we don't market anywhere.

### 2.2 Trends explicitly to ignore

- **Agent marketplaces / directories** (Aider Skills Hub, GPT Store analog for coding agents). rousseau is a personal daemon; joining a marketplace is scope creep.
- **Multi-agent orchestration frameworks** (Swarm, CrewAI, AutoGen 2). Solving a real problem for enterprises, not for a solo user. If we need it we already delegate via `claude` (which has subagents).
- **Cloud-hosted rousseau-as-a-service.** Turns the project into a SaaS company. Not the design.
- **UI for skill authoring**. VS Code + agentskills extension owns this. Not our job.

## 3. The remaining gap list, honestly

Ten categories from the original scorecard, updated. Score is realistic; blocker is what would move it.

| # | Category | Now | Blocker to 10 | Cost |
|---|---|---|---|---|
| 1 | Core correctness | 9 | Wall-clock time under load — no code fixes it | free (waits) |
| 2 | Documentation | 10 | – | – |
| 3 | Test coverage | 9 | Cobra RunE integration harness; ~600 statements remain | 2 days |
| 4 | Security posture | 9 | Reproducible builds; kernel-level egress rules; nftables example that actually works | 1 day |
| 5 | Feature breadth | 9 | Bedrock + Vertex providers; browser-tool shim; structured-output helper | 3 days |
| 6 | Performance | 10 | – (benchmarks in place, floor at 121ns/op on hot path) | – |
| 7 | Deployment | 10 | – (Quadlet + SLSA-3 + SBOM) | – |
| 8 | Codebase quality | 10 | – (files bounded; layered dependencies; 100% godoc) | – |
| 9 | Developer experience | 10 | – (`make check` mirrors CI; templates; benchmarks; fuzz) | – |
| 10 | Ecosystem fit (2026) | 10 | – (MCP + streaming + skills all shipped) | – |

**Aggregate: 96/100** — up from 89 after the last analysis.

## 4. What "top of category" actually means

The honest framing: rousseau is not competing with Devin (cloud SWE agent, $500M funded) or Cursor (10M+ users, IDE-embedded) — different categories. It **can** be the reference implementation of:

> _"A single-binary, self-hosted, container-native, personal coding agent reachable from any messaging app you already use, with zero paid infrastructure."_

Nobody else occupies this niche. Hermes is close but 8× larger and Python-heavy. OpenHands is bigger, more general, less personal. goose is a framework, not a product.

**What "top of niche" would concretely require:**

1. **A one-line install** (`curl | sh`) that works on Ubuntu, Fedora, Arch, macOS. Currently requires cloning + `make build` + writing a Quadlet unit by hand.
2. **A demo video / first-run wizard** that a stranger can follow in 5 minutes. Currently a stranger needs to read README + docs/ + walk through Podman + WhatsApp pairing.
3. **A public review by someone credible.** No code fixes this — a positive Hacker News post or a mention in [thesephist.com](https://thesephist.com) or on the Charm blog does.
4. **The three gaps in §3 that are `<3 days cost`**: reproducible builds (security), Bedrock+Vertex (breadth), Cobra RunE harness (coverage). All doable in a week.

## 5. Phase plan

### Phase A — one-line install (biggest visibility win)

**Goal**: `curl -sSL https://raw.githubusercontent.com/sebastienrousseau/rousseau-agent/main/install.sh | sh` gives you a running daemon.

- `scripts/install.sh` — detects OS, installs Podman (if missing), builds the container image, drops the Quadlet unit into `~/.config/containers/systemd/`, prompts for the WhatsApp pairing, prints the QR.
- `scripts/uninstall.sh` — reverse operation.
- README: replace "clone + make build" with "curl install → scan QR → done".

**Estimate**: 1 day. **Impact**: 10× lower onboarding friction.

### Phase B — reproducible builds + kernel-level egress

- Pin the Alpine base image by digest (not tag). Same for `node:22-alpine`.
- Add `.github/workflows/reproducible-build.yml` that builds the container twice from a clean checkout and diffs the digests.
- `docker/nftables.example.conf` — a copy-pasteable ruleset that pins outbound to Meta + Anthropic + OpenAI + Signal + Telegram IP ranges. Include instructions and a `Verify:` block that shows how to test it.
- Wire the reproducible-build workflow into the release process so every tag ships with a reproducibility attestation.

**Estimate**: 1 day. **Impact**: closes security 9→10.

### Phase C — Bedrock + Vertex providers

- `internal/llm/bedrock/` using `aws-sdk-go-v2/service/bedrockruntime`. AWS SigV4 flows are handled by the SDK; user config is `provider: bedrock` + `bedrock: {region, model, credentials_profile}`.
- `internal/llm/vertex/` using `cloud.google.com/go/vertexai/genai`. ADC-first; user config is `provider: vertex` + `vertex: {project, region, model, credentials_file (optional)}`.
- HTTP-fixture tests for both (using the SDKs' testable HTTP-client hooks).
- Update `docs/COMPETITORS.md` feature matrix.

**Estimate**: 2 days. **Impact**: closes breadth 9→10.

### Phase D — Cobra RunE integration harness

- Extract a thin `RunE` context struct that Cobra RunE closures unpack. Tests instantiate it with fakes for whatsmeow / signal-cli / telegram HTTP.
- Backfill tests for `whatsapp`, `signal`, `telegram` command RunE closures — the ~600 uncovered statements.
- Overall coverage target: 90%+.

**Estimate**: 2 days. **Impact**: closes test coverage 9→10.

### Phase E — Structured-output helper

- `agent.StructuredOutput` — provider-agnostic wrapper that takes a JSON schema, an `interface{}` target, and an `agent.Request`; runs the completion with schema-constrained mode when the provider supports it (Anthropic tool-use, OpenAI response_format, Gemini responseSchema) and falls back to prompted-with-example otherwise.
- Ships with 100% coverage on the pure logic paths.

**Estimate**: 1 day. **Impact**: 2026 ergonomics; +1 on ecosystem fit toward preserving the 10.

### Phase F — Demo video / first-run wizard

- `rousseau init` interactive command: asks provider, prompts for keys (or verifies claudecli), asks about WhatsApp / Signal / Telegram, writes a starter `config.yaml`, launches the daemon.
- 60-second screencast for the README: install → pair → text yourself → get reply. Committed as an asset under `docs/media/`.

**Estimate**: 1 day. **Impact**: onboarding.

### Phase G — Cache markers on direct anthropic provider

- Detect messages that survived compression and mark them with `cache_control: {type: "ephemeral"}`.
- Track cache hit rate as a `agent.compression.cache_efficiency` metric surfaced via `rousseau status`.

**Estimate**: half a day. **Impact**: real cost savings for pay-per-token users; +1 on 2026 trend fit.

## 6. The honest 10/10 verdict

After Phase A–D land, rousseau will genuinely score **10/10** across every measurable category in the scorecard. That does not make it "top of industry" in the sense of beating Devin or Cursor — different problems — but it makes it the honest, obvious answer to the question "what should I run for a self-hosted coding agent I can reach from my phone".

The remaining ceiling above that is not code. It's:

1. **Users.** Someone who isn't me needs to actually use it and blog about it. 
2. **Time under load.** Bugs surface when 100 people run this daemon for 6 months. Nothing accelerates that except waiting.
3. **Contributor community.** OSS momentum → trust → adoption. Requires public discussion.

Those three cannot be bought in a git commit. Everything else can.

## 7. Suggested execution order

If you do this over one week of focused work:

- **Day 1**: Phase A (install script) — biggest visible impact
- **Day 2**: Phase B (reproducibility + egress)
- **Days 3-4**: Phase C (Bedrock + Vertex)
- **Day 5**: Phase D (RunE harness) + Phase G (cache markers)
- **Weekend**: Phase E (structured output) + Phase F (demo video)

Each phase is a standalone PR. Each closes a category. At the end of week 1, every scorecard category is 10.

Then the work becomes non-code: publish, get feedback, iterate on real users' pain points, wait.
