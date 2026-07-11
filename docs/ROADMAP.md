# rousseau-agent — implementation plan

_Last touched: 2026-07-11 (commit at HEAD)._

This file is the living implementation plan for `rousseau-agent`. It is the source of truth for scope, priority, and sequencing. Ship diffs against this doc, not against verbal plans that vanish.

Sections:

1. [What is done](#1-what-is-done)
2. [What is next — Q3 2026](#2-what-is-next--q3-2026)
3. [What is next — Q4 2026](#3-what-is-next--q4-2026)
4. [Deferred / not-doing](#4-deferred--not-doing)
5. [Non-negotiable engineering standards](#5-non-negotiable-engineering-standards)
6. [How to update this plan](#6-how-to-update-this-plan)

---

## 1. What is done

### 1.1 Core

- Layered domain (`agent/`) with `Provider`, `Message`, `Session`, `Turn` — no import cycles, consumer-defined interfaces.
- Two LLM providers: `claudecli` (subprocess, inherits Claude Code auth) and `anthropic` (direct API, exact-pinned SDK).
- Persistent claude-session cache across daemon restarts (in-memory + SQLite-backed).
- `Session` UUID → `claude --session-id` → `--resume` fallback when claude has state from a prior run.

### 1.2 Tools

- Registry with sorted `Names`, `Definitions`, safe concurrent registration.
- Five built-in tools: `read`, `write`, `edit`, `grep`, `bash`. All with strict JSON-schema inputs. `edit` refuses non-unique `old_string`; `grep` skips `.git`/`node_modules`/`vendor`/binary files and caps result count with an explicit truncation notice.

### 1.3 Storage

- SQLite via `modernc.org/sqlite` (pure Go, no CGO). WAL journaling, `busy_timeout=15s`, `synchronous=NORMAL`, `foreign_keys=ON`.
- Tables: `sessions`, `jid_sessions` (transport → session mapping), `claude_sessions` (provider cache), `cron_jobs`.
- FTS5 virtual table (`sessions_fts`) with porter + unicode61 tokenizer and INSERT/UPDATE/DELETE triggers keeping it in sync.

### 1.4 Transports

- `transport.Transport` interface, `Router` with per-JID sessions, allowlist gating.
- WhatsApp bridge via `go.mau.fi/whatsmeow`. QR pairing, session persistence, LID → account-JID substitution, own-device loop prevention, multi-device suffix stripping, live typing indicator via `ChatPresence`, branded `💎 *Rousseau Agent*` reply header, voice-note transcription hook (whisper.cpp shell-out; disabled by default), unattended-daemon permission-mode auto-default.

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
- Coverage: **71.3%** overall — `agent` 86%, `tools` 100%, `config` 95%, `claudecli` 82%, `tui` 87%, `whatsapp` 55% (whatsmeow connection init untestable in-process), `state/sqlite` 76%.
- Godoc coverage: **100%** on exported identifiers (`revive [rule.exported][rule.package-comments]` clean).
- `goreleaser` for cross-platform binaries.

---

## 2. What is next — Q3 2026

Priority order. Each item lists **scope**, **exit criteria**, and **estimate** (senior-solo weeks).

### 2.1 ~~Cron scheduler goroutine~~ ✅ shipped

Landed in commit that added this ROADMAP note. `internal/cron/scheduler.go` runs jobs on their schedule, delivers via WhatsApp, records `last_run_at`. Poll interval reconciles the running entries with the store so `cron add` / `cron enable` become live within one poll.

### 2.2 ~~Anthropic provider streaming~~ ✅ shipped

Landed. `internal/llm/anthropic/stream.go` implements `agent.StreamingProvider` via the SDK's `NewStreaming` iterator. Same `StreamEvent` / `StreamReport` shape as claudecli.

### 2.3 TUI streaming (P1)

**Scope.** Wire `Provider.Stream` into the Bubble Tea model so tokens render as they arrive. Replace the current `busy` bool + spinner with an incremental viewport update. Retain `Ctrl+C` interrupt semantics.

**Exit criteria.** `rousseau chat` shows text rendering left-to-right during long generations. Interrupt drops the partial and returns to prompt.

**Estimate.** 3 days.

### 2.4 ~~Approval + policy gate for tool use~~ ✅ shipped

Landed. `agent.Approver` interface + three built-ins (`AllowAll`, `DenyAll`, `Pattern`). Consulted in `agent.runTools` before every execution; denials surface as `tool_result{is_error: true}`. Config surface: `agent.approver.{mode, reason, default, allow, deny}`. Coverage on the new types: ~92%.

Deferred to a follow-up: interactive approver (TUI-only) — needs a UX pass on how to display + accept/deny in the model loop without breaking streaming.

### 2.5 ~~Multi-provider registry~~ ✅ shipped

Landed. `internal/llm/openai/` implements the OpenAI Chat Completions API and, via `BaseURL`, serves OpenRouter, ollama, LM Studio, together.ai — any OpenAI-compatible endpoint.

`provider: openrouter`, `provider: openai`, or `provider: ollama` in config swaps the backend. Defaults (`openrouter.base_url`, `ollama.base_url`, `ollama.api_key`) mean the ollama and OpenRouter cases need only a `model:` line to work.

Google Vertex and AWS Bedrock deferred — they use SigV4 / OAuth flows the OpenAI shape does not cover; add when there is a concrete user for them.

### 2.6 Session compression & cross-session recall (P2)

**Scope.** Long conversations blow past the context window and cost more per turn. Add opt-in compression: when a session exceeds N tokens (measured by an approximate counter), summarise the oldest half via a compressor tool (`compression` provider or a dedicated LLM call), collapse into a synthetic user message, and prune raw messages. Persist both the compressed and raw forms for future recall.

Cross-session recall: on a new session's first turn, run FTS5 against recent sessions for keywords in the user message, prepend relevant snippets as context.

**Exit criteria.** `agent.compression.threshold_tokens` config; recall triggered by `agent.recall.enabled`. Tests demonstrate 10K-turn sessions still fit in the model's context. Recall test verifies snippets are prepended verbatim from the source session.

**Estimate.** 2 weeks.

---

## 3. What is next — Q4 2026

### 3.1 Second transport: Signal (P1)

**Scope.** Signal has an official protocol via `signal-cli` (a JVM daemon) or `libsignal-net` (Rust, HTTP surface). Prefer the latter to keep the Go binary self-contained. Implement `internal/transport/signal/` following the shape of `whatsapp/`.

**Exit criteria.** `rousseau signal` runs. Same allowlist / router / handler contract. Own JID equivalent, presence indicator equivalent (Signal lacks typing? — verify).

**Estimate.** 1.5 weeks.

### 3.2 ~~MCP server surface~~ ✅ shipped (pulled forward from Q4)

Landed. `internal/mcp/` implements a stdio JSON-RPC 2.0 server against MCP revision `2024-11-05`. Four read-only tools published:

- `rousseau_search_sessions` (FTS5 syntax)
- `rousseau_list_sessions`
- `rousseau_read_session`
- `rousseau_cron_list`

`rousseau mcp` starts the server. Verified end-to-end by piping raw JSON-RPC — real session data flows through.

Message-send tool intentionally omitted: the daemon owns the whatsmeow socket, and running a second process that opens the same store risks lock contention. If we need write access from MCP, we'll add a Unix-socket API on the daemon rather than expose it directly.

### 3.3 Skills / self-improving prompts (P2)

**Scope.** Read-only for now. Register the agentskills.io directory format so users can drop skills into `~/.local/share/rousseau/skills/` and the system prompt splices them in based on user-message topic classification.

**Exit criteria.** A skill file at `skills/git-workflow.md` gets injected into the system prompt when the user says "I need help with a rebase." Classification via cheap prompt to a fast model (claudecli's default is fine).

**Estimate.** 1.5 weeks.

### 3.4 Web dashboard (P2 — nice-to-have)

**Scope.** Small Vue/React dashboard for session browsing, cron editing, doctor reports. Skip until users ask.

**Estimate.** 3 weeks (only if prioritised).

---

## 4. Deferred / not-doing

Explicit "no" list — revisit only if the reason changes.

| Item | Why not |
|---|---|
| Full desktop app (Tauri/Electron) | The Podman/systemd deployment already covers "always-on daemon". A GUI is polish, not core. |
| Custom fork of `whatsmeow` | The upstream is actively maintained. A fork is maintenance debt with no material benefit today. |
| Fine-tuning / trajectory generation | rousseau is a runtime, not a training pipeline. Hermes has this; that is fine — Hermes ships there. |
| Bespoke browser automation toolset | Delegate to `claude` (which has built-in browser tools) or to an external MCP server. |
| Voice-note *response* (TTS) | Every mainline transport (WhatsApp, Signal) already renders text-to-speech client-side. Sending audio adds a media-upload path we do not want to own. |

---

## 5. Non-negotiable engineering standards

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

## 6. How to update this plan

- Move done items to §1.
- When priorities shift, edit §2 / §3 in-place. Do not accumulate historical priorities in the file — git holds that.
- If an item survives three review cycles without progress, either move it to §4 with a reason or split it into smaller items.
- Any deferral to a later quarter must state its blocker or opportunity cost.

Rejected pattern: "we might do X someday." Either it is in the plan with a P-number, or it is in §4 with a reason.
