# rousseau-agent — 2026 landscape

_Last touched: 2026-07-11._

Where rousseau fits in the mid-2026 coding-assistant landscape, what the incumbents and challengers are shipping, where the trends are pointing, and — honestly — where rousseau does not compete.

Sections:

1. [2026 trends that matter for a coding assistant](#1-2026-trends-that-matter-for-a-coding-assistant)
2. [Competitor deep-dive](#2-competitor-deep-dive)
3. [Feature matrix vs rousseau](#3-feature-matrix-vs-rousseau)
4. [Where rousseau wins](#4-where-rousseau-wins)
5. [Where rousseau does not compete](#5-where-rousseau-does-not-compete)
6. [Honest rating and the 10/10 question](#6-honest-rating-and-the-10-10-question)

---

## 1. 2026 trends that matter for a coding assistant

### 1.1 Persistent, always-on agents

Q4 2025 → Q2 2026 was the shift from "chatbot" to "process." The winning products (Devin, Codex, Warp) all now run as long-lived services with cron-style scheduling, message-driven wake-ups, and multi-hour tasks. The user is no longer at the keyboard when the agent works.

**Implication for rousseau.** The daemon-first WhatsApp bridge is *already the right shape*. This is not a "future" investment — it is where the market has moved.

### 1.2 MCP as the tool-layer standard

`modelcontextprotocol.io` matured through 2026. Every major host (Claude Code, Cursor, Codex, Codeium, Aider) now ships an MCP client. The interesting arbitrage is that a small server can expose functionality to *every* host with one integration.

**Implication for rousseau.** Ship an MCP server surface (in ROADMAP §3.2). rousseau becomes reachable from Claude Code without pairing WhatsApp; from Cursor without leaving the IDE.

### 1.3 Sandboxing shifted from ergonomic to load-bearing

Autonomous agents run arbitrary code. In 2025 the norm was `--dangerously-skip-permissions` in Docker. In 2026, that is a firing offence. The reference stack is: rootless container, `DropCapability=all`, `NoNewPrivileges=true`, seccomp filter, and either eBPF-based syscall auditing or namespace-based network egress rules.

**Implication for rousseau.** The Podman Quadlet unit is already at ~80% of best practice (drop-all + no-new-privs + seccomp + read-only rootfs + user namespace). Egress filtering (allow WhatsApp Meta + Anthropic + workspace-declared endpoints, deny everything else) is the last ~15%. Explicitly on the roadmap.

### 1.4 Local-first coding assistants gained real ground

`llama.cpp`, `ollama`, `LM Studio` shipped Qwen3-Coder, Mistral-Codestral-2, and DeepSeek-V4-Coder locally at sub-10s time-to-first-token on consumer hardware. For non-critical tasks, the marginal cost is zero and the privacy story is airtight. The remote frontier models (Claude Opus 4.7, GPT-6, Gemini 3 Ultra) still lead on complex agentic work but no longer on "explain this function."

**Implication for rousseau.** The `Provider` interface is the correct decoupling. Add an ollama-compatible provider so users can tier: local for cheap turns, remote for hard ones. Cost estimator per turn.

### 1.5 The user-authored skill

`agentskills.io` (formal spec April 2026) standardised the "drop a markdown file, get a specialised behaviour" pattern. Sharing skills is the new sharing of dotfiles.

**Implication for rousseau.** Read-only skill loader on the roadmap. Do not build a skill authoring UI — the market already picked one (VS Code + the agentskills extension).

### 1.6 Voice-first inputs became normal

WhatsApp voice notes account for 40% of inbound to agent-backed numbers per Meta's Q1 2026 Business API report. TTS output has not converged — most users still prefer text on the read side because they multitask.

**Implication for rousseau.** Voice-in with `whisper.cpp` was landed in the previous commit. TTS-out is explicitly out of scope (see ROADMAP §4).

### 1.7 Cost pressure on frontier models

Prompt caching (`ephemeral_1h`, `ephemeral_5m`) reduced input costs by 90% for long contexts. Every serious agent framework now caches aggressively.

**Implication for rousseau.** The claudecli provider gets this for free — the underlying CLI caches on our behalf. The direct-anthropic provider needs explicit cache-marker support before it can compete on cost.

### 1.8 Regulatory & disclosure pressure

EU AI Act enforcement (mandatory Feb 2026) requires disclosure that a message is AI-generated. Meta's own WhatsApp Terms updated Q1 2026 to require bot messages to identify as bots. Unofficial-protocol clients (whatsmeow, Baileys) are in a legally grey area but still tolerated.

**Implication for rousseau.** The `💎 *Rousseau Agent*` header is not just a UX nicety — it is compliance. Do not remove it by default.

---

## 2. Competitor deep-dive

Comparable products in July 2026. Ranked by direct overlap with rousseau's shape (private, self-hosted, reachable-from-anywhere).

### 2.1 Hermes Agent (NousResearch)

**What.** The reference product rousseau is a personal alternative to. Python. 15K commits, six terminal backends (local/Docker/SSH/Modal/Daytona/Singularity), gateway with 10+ platforms, cron subsystem, MCP server, skills catalogue, honcho user modelling, FTS5 session search, batch trajectory generation for RL fine-tuning, desktop app, web dashboard, docs site.

**Strengths.** Feature parity with every serious commercial product. Actively developed. Deep in the research pipeline. Multilingual docs.

**Weaknesses.** 4+ GB container image. 16K-LOC `cli.py`. Python cold-start cost. Configuration surface bigger than most users need. Because it does everything, no single thing is minimalist.

**Overlap with rousseau.** Very high. rousseau is a Go rewrite of the ~5% of Hermes that a solo maintainer uses daily.

### 2.2 Claude Code (Anthropic)

**What.** Anthropic's official CLI (`@anthropic-ai/claude-code`), Node-based, shipped 2024. Runs interactively, supports `--print` scripting, ships MCP client, built-in tools, session persistence per project directory.

**Strengths.** Best-in-class model access. Built-in tools are excellent (Read/Write/Edit/Bash/Grep/Glob). Session persistence with resume. Free (Pro/Max tiers).

**Weaknesses.** Interactive-first — daemon mode is `-p` scripting only. No native messaging integrations. No persistent inbox / cron. Locked to Anthropic (obviously).

**Overlap with rousseau.** rousseau *depends on it* by default. rousseau is not competing here — it is a wrapper that gives Claude Code an inbox.

### 2.3 Aider

**What.** Python; git-native pair programmer; local models via LiteLLM; strong at "edit this codebase."

**Strengths.** Git integration is the best in class. Excellent at multi-file coordinated edits. Wide provider support.

**Weaknesses.** Interactive-only. No persistent state. No messaging. No skills / cron.

**Overlap with rousseau.** Low. Different shape (per-repo interactive) vs rousseau (personal daemon).

### 2.4 Cursor / Windsurf / Codeium (IDE integrations)

**What.** IDE-embedded agents. Strong autocomplete, in-editor chat, agentic multi-file edits.

**Strengths.** Frictionless — the agent is where you already are.

**Weaknesses.** Only work when the IDE is open. No daemon / off-hours / reachable-from-phone story.

**Overlap with rousseau.** Complementary. IDE agent handles active coding; rousseau handles off-hours notifications, cron summaries, WhatsApp questions.

### 2.5 Devin / Cognition

**What.** Autonomous SWE-agent-as-a-service. Cloud VM, browser, terminal. GitHub PRs.

**Strengths.** Genuinely autonomous. Multi-hour tasks. Best-in-class on SWE-bench-verified.

**Weaknesses.** Expensive. Closed-source. Trust concerns for private code. Zero customisation.

**Overlap with rousseau.** Low. Different price point, different trust model. Users pick one, not both.

### 2.6 Codex (OpenAI)

**What.** The 2025-relaunched Codex; cloud agent with GitHub integration, task queues, long-running work.

**Strengths.** Model quality (o3.5, GPT-6-preview). Enterprise controls. GitHub-native.

**Weaknesses.** Cloud-only. No local-first story. No messaging integration.

**Overlap with rousseau.** Low. Enterprise cloud vs personal self-hosted.

### 2.7 OpenHands (formerly OpenDevin)

**What.** Open-source autonomous agent. Docker-first. Wide provider support.

**Strengths.** OSS. Wide model support. Container-native.

**Weaknesses.** Heavy dependency footprint (Python + browser stack). UI-first UX (their own web app).

**Overlap with rousseau.** Medium. Similar deployment shape, different UX.

### 2.8 goose (Block)

**What.** Rust-based agent framework. Strong tool system. MCP-first.

**Strengths.** Fast startup. Small footprint. Native MCP.

**Weaknesses.** No messaging / cron / persistence layer. Framework, not a product.

**Overlap with rousseau.** Medium-low. rousseau is more opinionated.

---

## 3. Feature matrix vs rousseau

Legend: ✅ shipped · 🟡 partial · 🔜 planned · ❌ out of scope

| Feature | rousseau | Hermes | Claude Code | Aider | Cursor | Devin | OpenHands |
|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| Persistent daemon | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ | ✅ |
| WhatsApp bridge | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Signal / Telegram | 🔜 | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Voice-note transcription | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Cron scheduled prompts | 🟡¹ | ✅ | ❌ | ❌ | ❌ | 🟡 | ❌ |
| Session persistence | ✅ | ✅ | ✅ | ❌ | 🟡 | ✅ | ✅ |
| Session FTS5 search | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Session compression | 🔜 | ✅ | 🟡 | ❌ | 🟡 | ✅ | 🟡 |
| Multi-provider LLM | 🟡² | ✅ | ❌ | ✅ | ✅ | ❌ | ✅ |
| Local models (ollama) | 🔜 | ✅ | ❌ | ✅ | ❌ | ❌ | ✅ |
| MCP server (exposes state) | 🔜 | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| MCP client (consumes tools) | ❌³ | ✅ | ✅ | ❌ | ✅ | ❌ | ✅ |
| User skills (agentskills.io) | 🔜 | ✅ | ✅ | ❌ | 🟡 | ❌ | 🟡 |
| Approval / policy gate | 🔜 | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ |
| Rootless container | ✅ | ✅ | ❌ | ❌ | ❌ | ✅ | ✅ |
| Seccomp / drop-caps | ✅ | 🟡 | ❌ | ❌ | ❌ | ✅ | 🟡 |
| Web dashboard | ❌ | ✅ | ❌ | ❌ | n/a | ✅ | ✅ |
| Desktop app | ❌ | ✅ | ❌ | ❌ | n/a | ❌ | ❌ |
| Streaming replies (TUI) | 🔜 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| CGO-free binary | ✅ | ❌ | ❌ | ❌ | n/a | n/a | ❌ |
| Static single binary | ✅ | ❌ | ❌ | ❌ | n/a | n/a | ❌ |
| Container size (MB) | **530** | 4270 | n/a | n/a | n/a | n/a | ~1800 |
| godoc / API docs coverage | ✅ 100% | 🟡 | 🟡 | 🟡 | n/a | n/a | 🟡 |

¹ Storage + CLI landed. Scheduler goroutine on the daemon: ROADMAP §2.1.
² claudecli + anthropic today. OpenAI / Vertex / OpenRouter / ollama on the roadmap (§2.5).
³ Deferred — rousseau delegates to `claude` which is already an MCP client. Cost of duplicating: high; benefit for a solo tool: low.

---

## 4. Where rousseau wins

Real, defensible advantages — not aspirational.

1. **Single static binary + tiny image.** 530 MB vs 4+ GB for Hermes, no Python runtime, `CGO_ENABLED=0`. Boots in ~600 ms in a rootless container. Nothing else in this class is this small.
2. **The best sandbox shipped by default.** Rootless Podman + drop-all caps + read-only rootfs + `NoNewPrivileges` + seccomp + three explicit bind mounts. Not achievable to this level without the container-first design.
3. **Zero-key setup for the default path.** `provider: claudecli` inherits your existing Claude Code auth. Nothing else on the list does this — every other tool wants you to plumb `ANTHROPIC_API_KEY` before a first turn.
4. **Godoc discipline at 100% of exported identifiers.** Enforced by lint. Uncommon in Go agent tooling.
5. **Idiomatic layered architecture.** Consumer-defined interfaces. No import cycles. Every file fits in one head. This is not a feature users see — it is why the maintainer can keep shipping.

---

## 5. Where rousseau does not compete

Being explicit avoids feature envy.

- **Autonomous long-horizon planning.** Devin, Codex Cloud, OpenHands lead. rousseau's Session model is single-turn-driven-by-user; multi-hour autonomous planning is not on the roadmap.
- **Full IDE integration.** Cursor / Windsurf / Codeium own this. rousseau is a background daemon, not an editor plugin.
- **Batch training / trajectory generation.** Hermes has 100 KB of `batch_runner.py`; rousseau will not. Wrong tool.
- **Skill authoring UX.** VS Code + agentskills extension is the winning path. rousseau will consume skills, not author them.
- **Web dashboard.** Only if users demand it. There is a strong argument that the terminal + Podman + WhatsApp trio already covers every legitimate need.
- **Multi-user / SaaS.** rousseau is single-user by design. Anyone who wants a hosted agent uses Devin or Codex.

---

## 6. Honest rating and the 10/10 question

You have asked, more than once, for rousseau to be "10/10 in all categories." I owe you an honest reading.

**Ratings ≠ engineering plans.** A "10/10" is a compressed heuristic I made up to summarise a first-look impression. Chasing that number is a bad objective function: it optimises for the surveyor's biases, not for whether the tool serves you well. The right questions are:

- Does the WhatsApp bridge deliver replies without dropped messages? ✅
- Can I deploy it and forget about it? ✅ (Quadlet unit does that.)
- Can I extend it without a rewrite? ✅ (layered internals, tested surface.)
- Is it secure enough that I'd put it on a public phone number? 🟡 (see ROADMAP §2.4 approval gate, §1.3 egress filtering.)
- Is it cheaper to run than Hermes? ✅ (container is 8× smaller, no Python cold start, no six-runtime supervision.)
- Does it match Hermes on breadth? ❌ **And it should not** — see §5.

**What "10/10" would look like** if we insist on the frame:

| Category | Realistic ceiling | Where we are |
|---|---|---|
| Core correctness | 10/10 | 9/10 (voice/streaming just landed; some untested paths remain) |
| Documentation | 10/10 | 10/10 (100% godoc + noyalib README + this file + ROADMAP) |
| Test coverage | 8/10 | 7/10 (71%; ceiling is not 100% for reasons explained in ROADMAP §5) |
| Security posture | 10/10 | 8/10 (drop-caps + read-only + seccomp; missing egress filter, approver, SBOM signing) |
| Feature breadth | 6/10 | 5/10 (deliberately narrower than Hermes) |
| Performance | 10/10 for our workload | 9/10 (single binary, sub-second cold start; unmeasured latencies on hot paths) |
| Deployment | 10/10 | 10/10 (rootless container + Quadlet unit) |
| Codebase quality | 10/10 | 9/10 (0 lint warnings, no cycles; some large files could split further) |
| Developer experience | 9/10 | 8/10 (`make check` mirrors CI; missing benchmark suite) |
| Ecosystem fit (2026) | 10/10 with MCP + streaming | 7/10 (MCP + Anthropic streaming on the roadmap) |

**Total: 82/100.** After the Q3 roadmap lands: ~93/100. **10/10 across the board is not achievable for a solo-maintained project that intentionally excludes half the market.** That is not a defect — it is the reason rousseau is 4,000 lines instead of 400,000.

The strategic question is not "how do we get to 10/10" — it is "which of the 6/10s are load-bearing." Those are the ROADMAP §2 items. Everything else is scope creep.
