# rousseau-agent — implementation plan

_Last touched: 2026-08-29 (commit at HEAD)._

This file is the living implementation plan for `rousseau-agent`. It is the source of truth for scope, priority, and sequencing. Ship diffs against this doc, not against verbal plans that vanish.

Sections:

1. [What is done](#1-what-is-done)
2. [What is next](#2-what-is-next)
3. [Deferred / not-doing](#3-deferred--not-doing)
4. [Non-negotiable engineering standards](#4-non-negotiable-engineering-standards)
5. [How to update this plan](#5-how-to-update-this-plan)

---

## 1. What is done

### 1.1 Core

- Layered domain (`agent/`) with `Provider`, `Message`, `Session`, `Turn` — no import cycles, consumer-defined interfaces.
- Two LLM providers: `claudecli` (subprocess, inherits Claude Code auth) and `anthropic` (direct API, exact-pinned SDK).
- Persistent claude-session cache across daemon restarts (in-memory + SQLite-backed).
- `Session` UUID → `claude --session-id` → `--resume` fallback when claude has state from a prior run.

### 1.2 Tools

- Registry with sorted `Names`, `Definitions`, safe concurrent registration.
- Six built-in tools: `read`, `write`, `edit`, `grep`, `bash`, `spawn_subagent`. All with strict JSON-schema inputs. `edit` refuses non-unique `old_string`; `grep` skips `.git`/`node_modules`/`vendor`/binary files and caps result count with an explicit truncation notice; `spawn_subagent` is exposed via the sub-agent fan-out shipped with §1.11 (see below).

### 1.3 Storage

- SQLite via `modernc.org/sqlite` (pure Go, no CGO). WAL journaling, `busy_timeout=15s`, `synchronous=NORMAL`, `foreign_keys=ON`.
- Tables: `sessions`, `jid_sessions` (transport → session mapping), `claude_sessions` (provider cache), `cron_jobs`.
- FTS5 virtual table (`sessions_fts`) with porter + unicode61 tokenizer and INSERT/UPDATE/DELETE triggers keeping it in sync.

### 1.4 Transports

- `transport.Transport` interface, `Router` with per-JID sessions, two-layer allowlist gating (Router-level for the agent loop plus a pre-reaction gate inside each transport's Dispatch to prevent bot-presence leaks to strangers).
- WhatsApp bridge via `go.mau.fi/whatsmeow`. QR pairing, session persistence, LID → account-JID substitution, own-device loop prevention, multi-device suffix stripping, live typing indicator via `ChatPresence`, branded `💎 *Rousseau Agent*` reply header, voice-note transcription hook (whisper.cpp shell-out; disabled by default), unattended-daemon permission-mode auto-default, image ingestion with per-image + per-turn byte caps + MIME allowlist.
- Six additional transports with the same Dispatch shape + image ingestion + Router-integrated allowlist: Slack, Discord, Telegram, Matrix, Signal (via `signal-cli` JSON-RPC), iMessage (via BlueBubbles), plus SMS + email variants.
- Tier-1/2/3 live progress UX on WhatsApp: emoji-reaction ack, sequential Claude-CLI-style bullet feed (one message per action with a 2s coalescing floor), ✅/❌ completion reaction. Reply is always a fresh message (never an edit of the placeholder) so notifications fire and the running log stays as thread history.
- Cross-transport identity: `/whoami`, `/link`, `/unlink` commands resolve one identity across every transport a user signs in from.

### 1.5 UI surfaces

- `rousseau chat` — Bubble Tea TUI with viewport + textarea + spinner.
- `rousseau whatsapp` — foreground daemon; the main runtime. Runs the WhatsApp bridge **and** the cron scheduler in the same process.
- `rousseau doctor` — diagnostics table (build, provider, state, whatsapp, config).
- `rousseau session {list,search,show,delete}` — FTS5-backed history browser.
- `rousseau cron {add,list,remove,enable,disable}` — scheduled prompts. Storage + CLI + scheduler goroutine all live. Enabled jobs fire on schedule, run the prompt through the configured provider, deliver via WhatsApp to `deliver_to`, and stamp `last_run_at`.
- `rousseau version` — build stamp.

### 1.6 Approval gate

- `agent.Approver` interface consulted before every tool execution. `AllowAllApprover` (default), `DenyAllApprover`, and `PatternApprover` (regex Allow/Deny rules; deny beats allow; unmatched requests fall back to `Default`).
- Configurable via `agent.approver.{mode, reason, default, allow, deny}` — no code change required to lock down `bash` or blanket-deny a tool.
- Denials surface to the model as `tool_result{is_error: true}` so the model can pick a different action rather than crashing.

### 1.7 Streaming providers

- `agent.StreamingProvider` optional interface returning `<-chan StreamEvent + <-chan StreamReport`.
- `claudecli` implements it via `--output-format stream-json`.
- `anthropic` implements it via the SDK's `NewStreaming` iterator; text deltas + tool-use starts are emitted as they arrive.
- Consumers (currently: none in the daemon; TUI streaming is planned) detect support with a type assertion.

### 1.8 Deployment

- `docker/Dockerfile` — multi-stage; ~530 MB image with claude CLI baked in.
- `docker/rousseau-agent.container` — Podman Quadlet unit: read-only rootfs, `DropCapability=all`, `NoNewPrivileges=true`, seccomp filter, `UserNS=keep-id`, three bind mounts (workspace RW, rousseau state RW, `~/.claude` RW).

### 1.9 Quality gates

- `go vet`, `golangci-lint v2` (strict), race-enabled tests on Linux + macOS, `govulncheck`, CodeQL, Dependabot for `gomod` + `github-actions`.
- Coverage: **package-level average ≈97.7%** as of 2026-08-29 — `agent` 96.5%, `progress` 99.7%, `transport/whatsapp` 96%+, `control` 100%, most transports ≥90%. The old outliers (55% on whatsapp, 76% on state/sqlite) were closed as separate coverage-hardening PRs across 2026-08.
- Godoc coverage: **100%** on exported identifiers (`revive [rule.exported][rule.package-comments]` clean).
- `goreleaser` for cross-platform binaries.
- Reproducible-build determinism gate: nightly rebuild against the same source produces bit-identical binaries.

### 1.10 Q3 + Q4 2026 quarterly plan — shipped in full

The Q3/Q4 items that used to occupy §2 and §3 of this doc are all landed. Locations if you want the code:

- **Cron scheduler goroutine** — `internal/cron/scheduler.go` runs jobs on their schedule, delivers via WhatsApp, records `last_run_at`. Poll interval reconciles running entries with the store so `cron add` / `cron enable` become live within one poll.
- **Anthropic provider streaming** — `internal/llm/anthropic/stream.go` implements `agent.StreamingProvider` via the SDK's `NewStreaming` iterator. Same `StreamEvent` / `StreamReport` shape as claudecli.
- **TUI streaming** — `agent.Agent.TurnStream` drives each provider round-trip through `StreamingProvider.Stream`; `tui.Model` consumes deltas via a `deltaPump` Cmd chain and renders text token-by-token. Falls back to `Turn` for non-streaming providers.
- **Approval + policy gate** — `agent.Approver` interface + `AllowAll` / `DenyAll` / `Pattern` built-ins, consulted in `agent.runTools` before every execution. Config surface: `agent.approver.{mode, reason, default, allow, deny}`. Denials surface as `tool_result{is_error: true}`.
- **Multi-provider registry** — `internal/llm/openai/` covers OpenRouter, ollama, LM Studio, together.ai, any OpenAI-compatible endpoint via `BaseURL`. Backend swaps by config key.
- **Session compression** — `internal/agent/compressor.go` with `LLMCompressor` that summarises the oldest slice into a synthetic `[rousseau-compressed]` marker message. Config: `agent.compression.{enabled, trigger_messages, keep_recent, prompt}` (defaults 60/8).
- **Cross-session recall** — `internal/agent/recall.go` extracts keywords from the latest user message and composes hits as a `# Related prior sessions` appendix.
- **Signal transport** — `internal/transport/signal/` shells out to `signal-cli --output=json -a <account> jsonRpc`. Same Router / allowlist / handler contract as WhatsApp.
- **MCP server surface** — `internal/mcp/` implements stdio JSON-RPC 2.0 against MCP revision `2024-11-05`. Read-only tools: `rousseau_search_sessions`, `rousseau_list_sessions`, `rousseau_read_session`, `rousseau_cron_list`. `rousseau mcp` starts the server.
- **Skills / self-improving prompts** — `internal/skills/` loads Markdown+YAML skills from `~/.local/share/rousseau/skills/`; substring-matched triggers splice the skill body into the system prompt as an `# Active skills` appendix.

### 1.11 5-week competitor-gap campaign (2026-07-16 → 2026-08-29) — shipped in full

The campaign in `docs/IMPLEMENTATION_PLAN_2026_07_16.md` landed in full. Header on that doc reflects the shipped status; it's retained for its engineer-level detail (file paths, function signatures, effort estimates). Landed items and their locations:

| # | Item | Location |
|---|------|----------|
| §1 | Native tool integrations | `internal/tools/integrations/{google,github,slack,linear,stripe,composio}` |
| §2 | OAuth broker + encrypted token store | `internal/auth/oauth/` |
| §3 | Slim runtime images | `docker/Dockerfile.distroless` + `docker/Dockerfile.lite` |
| §4 | Per-JID rate limiter | `internal/ratelimit/` |
| §5 | Panic recovery + circuit breaker | `internal/resilience/{recover,breaker}.go` |
| §6 | Redacting slog handler | `internal/observability/redact/` |
| §7 | Image ingestion (inbound) | `internal/transport/*/images*.go` — all 7 transports |
| §8 | Sub-agent parallelism | `internal/agent/subagent/` |
| §9 | Vector store + hybrid recall | `internal/recall/` + OpenAI/Voyage embedders + config wireup |
| §10 | Wall-clock correctness harness | `test/integration/soak/` (24h nightly on main, 30m per-PR) |
| §11 | Comparative docs (`WHY_NOT_*.md`) | `docs/WHY_NOT_{OPENCLAW,TRUSTCLAW,ZEROCLAW}.md` |

### 1.12 Post-campaign polish (2026-08)

Landed since the campaign closed:

- **Tier-1/2/3 live WhatsApp progress UX** — heartbeat placeholder → per-tool bullet feed → completion summary, with sequential mode (one WhatsApp message per action, 2s coalescing floor) so the thread reads chronologically instead of the bot silently editing itself.
- **Pre-reaction allowlist gate** — transport-level filter INSIDE `Dispatch` that drops non-allowlisted senders before ANY user-visible reaction. Fixes a privacy leak where the `👀` / `✅` acks revealed to strangers that the number was bot-monitored.
- **Docker/Quadlet host-key copy path** — `docker/scripts/rousseau-agent-copy-host-keys.sh` reads `~/.config/rousseau/host-keys.list` and populates a state-volume subtree the container symlinks to `~/.ssh` / `~/.gnupg`. Cross-platform (bash + `cp`); allowlist confined to `$HOME/.ssh/` and `$HOME/.gnupg/`.
- **Container OAuth token wiring** — `EnvironmentFile=%h/.config/rousseau/agent.env` on the Quadlet drop-in surfaces `GITHUB_TOKEN` inside the container so its `gh` and `git` operations authenticate as the operator.
- **Router streaming double-close fix** — `TurnStream` owns the events channel close (Go sender convention); Router.runTurn no longer double-closes. Fixed a per-turn panic that was silently swallowed by the resilience wrapper.
- **`rousseau-agent-claude-creds.path` narrowing** — watches only `.credentials.json`, not `~/.claude.json`. The latter is rewritten by every host-side Claude Code action and was killing in-flight WhatsApp turns every few seconds.
- **Interactive TUI approver** (`internal/tui/approver.go`) — `rousseau chat` prompts the user per tool call with `y / n / a / d` (allow / deny / always-allow / always-deny). Session-scoped memory means the second `bash` call after `[a]` runs without a prompt (with a small toast note). Chains under any config-driven `PatternApprover` (deny short-circuits; allow falls through to interactive). Daemon transports unchanged.
- **MCP client daemon version thread-through** — `mcpclient.Config.ClientVersion` populated from `cli.Version()`; MCP servers log the real rousseau version instead of hardcoded `"0.0.1"`.
- **Code hygiene: TODO → FUTURE for deferrals** — subsequently closed: sandbox and A2A design decisions landed as §1.12 items below. `grep -rn 'TODO' internal/ pkg/` returns zero hits in production code.
- **Sandbox argv policy** (`internal/tools/sandbox/{gvisor,nsjail}.go` + `Policy` struct in `sandbox.go`) — closes the deferred gvisor/nsjail argv shape. Both backends translate a `Policy` (NoNetwork, TmpdirRoot, Wallclock, CPUSeconds, MemoryBytes, Readonly, Writable) into their native flag surface. `DefaultPolicy()` ships the safe disposition (deny egress). Per-invocation tmpdir helper shared across backends. 95.7 % coverage on the sandbox package.
- **A2A artifact `Fetcher`** (`internal/a2a/fetch.go`) — closes the deferred `Task.InputArtifacts` fetch semantics. Three canonical URI schemes handled: `data:` (RFC 2397 parser), `http(s)://` (bounded GET with cross-origin redirect refusal), and `artifact://` (dispatched to caller-supplied `Resolver`). Per-artifact size cap (default 32 MiB) enforced on every branch.
- **Tag `v0.0.2`** cut against this state (per convention: only ever +0.0.1 increments).

---

## 2. What is next

The Q3 + Q4 quarterly plan and the 5-week competitor-gap campaign both shipped in full; §1.12 above covers post-campaign polish through v0.0.2. What remains is genuinely optional — all small, all deferred behind an explicit blocker.

### 2.1 Google Vertex + AWS Bedrock providers

Deferred from §1.10 multi-provider registry. Both use SigV4 / OAuth flows the OpenAI-compatible shape does not cover. Add when there's a concrete user request; low priority because OpenRouter already serves Anthropic/OpenAI/Gemini/Bedrock-Claude behind an OpenAI-compatible façade.

**Estimate:** 2 days each provider (Vertex ≈ 2, Bedrock ≈ 2).

### 2.2 libsignal-net Go bindings

Deferred from §1.10 Signal transport. Current implementation shells out to `signal-cli`; a direct binding removes the JVM + JSON-RPC hop. Waiting on upstream libsignal-net Go stability.

**Estimate:** 3–5 days once bindings are stable; skip until then.

### 2.3 Skill marketplace + agent-authored skills

Deferred from `docs/IMPLEMENTATION_PLAN_2026_07_16.md §12`. Requires (a) a signed manifest format for community skills, (b) a `~/.local/share/rousseau/skills/community/` sandbox with review-on-first-run semantics, (c) an agent-side pattern for writing a new skill from a repeated interaction. All three are separate design problems.

**Estimate:** ~1 week end-to-end; deferred until there's a genuine user pull.

### 2.4 Wiring the sandbox into `bash`

`internal/tools/builtin/bash.go` still does a direct `exec.CommandContext`; the sandbox package (§1.12) exists but no built-in tool consults it yet. Turning sandboxing on is a design decision, not a bug — it changes the default trust model for the bash tool. Blockers: (a) opt-in config surface (`tools.bash.sandbox: {kind, no_network, ...}`), (b) deprecation window for operators running today's un-sandboxed bash, (c) per-tool sandbox kind (a lightweight read/write/edit should not pay gvisor's syscall overhead).

**Estimate:** 2–3 days end-to-end.

---

## 3. Deferred / not-doing

Explicit "no" list — revisit only if the reason changes.

| Item | Why not |
|---|---|
| Full desktop app (Tauri/Electron) | The Podman/systemd deployment already covers "always-on daemon". A GUI is polish, not core. |
| Custom fork of `whatsmeow` | The upstream is actively maintained. A fork is maintenance debt with no material benefit today. |
| Fine-tuning / trajectory generation | rousseau is a runtime, not a training pipeline. Hermes has this; that is fine — Hermes ships there. |
| Bespoke browser automation toolset | Delegate to `claude` (which has built-in browser tools) or to an external MCP server. |
| Voice-note *response* (TTS) | Every mainline transport (WhatsApp, Signal) already renders text-to-speech client-side. Sending audio adds a media-upload path we do not want to own. |
| Web dashboard | `rousseau session {list,search,show}`, `rousseau cron list`, and MCP hosts (Claude Code / Cursor) already cover the same use cases. A browser UI is polish, not core. |

---

## 4. Non-negotiable engineering standards

Every commit and PR must uphold these. CI enforces the ones marked ✅.

- ✅ `go vet` clean.
- ✅ `golangci-lint` strict clean (no `fmt.Print*` in library code, no panics outside `main`).
- ✅ Race-enabled tests pass on Linux + macOS.
- ✅ `govulncheck` clean.
- ✅ CodeQL clean.
- ✅ 100% godoc on exported identifiers (`revive [rule.exported]`).
- ✅ Coverage does not drop below the previous commit.
- Every exported type has a rationale in the doc comment — "what and why," not "how."
- No `interface{}` / `any` in public APIs without a comment naming why.
- Contexts propagate through every I/O path.
- Errors wrap with `fmt.Errorf("scope: op: %w", err)`.
- No panics outside `main` and test helpers.
- Conventional Commits (`feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`, `ci:`).

Aspirational (not CI-enforced yet, but should be considered a bug when violated):

- Every new feature ships with at least one benchmarking function on its hot path.
- Every new tool ships with an entry in `examples/`.
- Every new command updates this ROADMAP and the root `README.md`.

---

## 5. How to update this plan

- Move done items to §1.
- When priorities shift, edit §2 in-place. Do not accumulate historical priorities in the file — git holds that.
- If an item survives three review cycles without progress, either move it to §3 (deferred) with a reason or split it into smaller items.
- Any deferral to a later quarter must state its blocker or opportunity cost.

Rejected pattern: "we might do X someday." Either it is in the plan with a subsection, or it is in §3 with a reason.
