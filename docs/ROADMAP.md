# rousseau-agent — implementation plan

_Last touched: 2026-08-30 (commit at HEAD)._

> **Commercial framing (2026-08-30):** the business model is now
> **open-core with a paid Enterprise / Team Edition** delivered as an
> offline license key inside the same static binary. See
> [`docs/COMMERCIAL.md`](COMMERCIAL.md) for the full contract —
> what's free, what's paid, and where the boundary lives in code
> (`internal/license`, `Checker.IsEnabled(feature)`). Every future
> ROADMAP entry must state whether it lands in the core or behind
> the licence gate; §2 below is being re-audited against that lens.

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
- **Sandbox argv policy** (`internal/tools/sandbox/{gvisor,nsjail}.go` + `Policy` struct in `sandbox.go`) — closes the deferred gvisor/nsjail argv shape. Both backends translate a `Policy` (NoNetwork, TmpdirRoot, Wallclock, CPUSeconds, MemoryBytes, Readonly, Writable) into their native flag surface. `DefaultPolicy()` ships the safe disposition (deny egress). Per-invocation tmpdir helper shared across backends. 100 % coverage on the sandbox package.
- **A2A artifact `Fetcher`** (`internal/a2a/fetch.go`) — closes the deferred `Task.InputArtifacts` fetch semantics. Three canonical URI schemes handled: `data:` (RFC 2397 parser), `http(s)://` (bounded GET with cross-origin redirect refusal + max-5-hop same-origin-only redirect policy), and `artifact://` (dispatched to caller-supplied `Resolver`). Per-artifact size cap (default 32 MiB) enforced on every branch.
- **Bash sandbox wire-up** (`internal/tools/builtin/bash.go` + `internal/cli/bash_sandbox.go`) — `BashTool` gains an optional `Sandbox` field; opt-in via `tools.bash.sandbox.kind` config. Zero config keeps the pre-sandbox behaviour byte-for-byte so nothing breaks on upgrade. Closes the last actionable §2 item that existed before the open-core recut.
- **Open-core license seam** (`internal/license/` + `docs/COMMERCIAL.md`) — offline Ed25519 signature-check runtime seam that separates the free OSS core from the paid Enterprise / Team Edition **inside the same static binary**. Preserves the "no phone home, no license server, no separate binary distribution" promise. Fails closed: expired / bad-signature / malformed / absent → free tier + `rousseau doctor` prints why. Three feature identifiers defined (`FeatureSSO`, `FeatureAuditEgress`, `FeatureGovernanceAdvanced`); nothing gates on them yet — the seam landed in isolation so downstream gate PRs can be reviewed on their own merits. 99.1 % coverage.
- **Coverage push to 98.6 % overall** — three targeted rounds lifting `resilience`, `tools/sandbox`, `cron`, `config` to 100 %; `tui` from 83.9 % to 98.2 %; smaller lifts on `a2a`, `llm/bedrock`, `whatsapp`, `mcp/client`. Remaining sub-98 % branches are either defensive guards the public API can't reach or external-service handshakes needing custom stdlib mocks.
- **Combined dependency bumps** — aws-sdk-go-v2/service/bedrockruntime 1.58.0, otel trace/sdk/otlptracehttp 1.46.0, docker/login-action v4, docker/build-push-action v7, actions/attest-build-provenance v4. Consolidated into one verified PR that superseded five conflicting dependabot PRs.
- **Doc consistency sweep** — coverage badge 98.81 % → 98.1 %; six-vs-five built-in tool count corrected; sandbox config example added to the README; `tools.bash.sandbox.kind` documented in the Capabilities table.
- **Tag `v0.0.2`** cut post-#92, **tag `v0.0.3`** cut post-#98 (per convention: only ever +0.0.1 increments).

---

## 2. What is next

Recut on 2026-08-30 through the [open-core lens](COMMERCIAL.md). Each item states whether it lands in the **core** (free OSS) or behind the **licence gate** (paid Team / Enterprise), and why. The Q3 + Q4 quarterly plan and the 5-week competitor-gap campaign have both shipped in full; §1.12 above covers post-campaign polish through v0.0.3.

**Sequencing:** items below are ordered by the 5-pillar ROI framework — commercial-critical work (§2.1 – §2.4) first because it unblocks paid-tier revenue; core-side quality-of-life follows (§2.5 – §2.7); genuinely-deferred items (§2.8 – §2.10) last.

---

### 2.1 First real licence gate — audit-egress pilot 🔒 **paid**

**Boundary:** `FeatureAuditEgress` (see [`docs/COMMERCIAL.md`](COMMERCIAL.md) §2.2).

Wire `license.Checker.IsEnabled(FeatureAuditEgress)` into the smallest concrete enterprise-shaped code path so the seam is proven end-to-end. Pilot pick: an OTLP-push audit-log sink under `internal/observability/audit_egress/` — smaller design surface than SSO (§2.2) and touches a package (`internal/observability`) with clean seams already.

The daemon boots; the OTLP sink attempts to start; the licence check fails → the sink logs `audit egress requires an Enterprise licence — see docs/COMMERCIAL.md` at INFO and stays disabled. The daemon proceeds normally. Paying customers set `ROUSSEAU_LICENSE_KEY` and the sink activates.

**Why first:** proves the whole open-core apparatus works before we invest in the bigger gated features. Also the smallest reviewable diff — one config surface, one gate call, one sink.

**Delivered (PR #113):** OTLP HTTP sink pilot with fail-open runtime discipline (drop records ≫ block daemon). Additive: PR #124 ships `audit_egress.ChainedSink` — hash-chained tamper-evident records for the SOC 2 / ISO 27001 / HIPAA audit-trail requirement listed on the COMMERCIAL.md gate boundary. Any Sink wraps into a ChainedSink; sequence + hash + prev_hash surface on the OTLP wire as `rousseau.audit.chain.*` attributes; `VerifyChain([]Record)` walks a batch offline and flags mutation / gap / reorder / insert. Daemon wiring: PR #125 exposes `observability.audit_egress.{kind,endpoint,headers,batch_size,flush_interval,queue_size,http_timeout,chained}` config, builds the sink from cfg + licence, wraps in `ChainedSink` when `chained: true`, emits `daemon.start` / `daemon.stop` boot/shutdown breadcrumbs so operators can verify their SIEM pipeline end-to-end with zero application activity, surfaces `AuditSink` on `daemonWiring` for future tool-call / auth instrumentation, and adds four `observability.audit_egress.*` doctor rows. Event instrumentation: PR #126 wires the sink into agent tool-calls (approver-deny, hook-deny, execute-success, execute-error records with Actor drawn from `sso.IdentityFromContext`) and into router `/login` / `/logout` (login-success / login-denied / login-error / logout-success). Combined, the operator's SIEM now sees every tool a paying customer's agent ran, and every SSO login attempt against the daemon. Cross-restart continuity: PR #127 adds `ChainStore` (SQLite `audit_chain_state` single-row table) so the chain resumes after daemon restarts instead of starting fresh at `sequence=0` — the SIEM sees one continuous chain across the whole daemon lifetime, matching the SOC 2 / ISO 27001 audit-trail-immutability expectation. License-state snapshot: PR #128 emits one `Category:license` audit record at daemon boot naming tier / subject / features / expiry / expiring flag, with `Result` classified as `core | invalid | expiring | active`. SIEM dashboards can now filter for "any daemon in the fleet running unlicensed / expiring this quarter" without joining against a separate source — Change-Management visibility for SOC 2 §CC7.1.

**Estimate:** 2 days end-to-end.

### 2.2 SSO adapters (OIDC + SAML) 🔒 **paid**

**Boundary:** `FeatureSSO` (see [`docs/COMMERCIAL.md`](COMMERCIAL.md) §2.1).

Add `internal/auth/sso/` with OIDC and SAML provider adapters, plus a mapping layer that resolves Slack / Matrix / Discord IDs to the identity's canonical `sub`. Gated on `FeatureSSO`. Local SQLite auth + API keys + `claudecli` OAuth stay in the core.

**Why this is the enterprise carrot:** compliance mandates SSO. Teams above ~10 seats will not deploy a chat agent without it. Highest-conversion feature per the framework's "expected returns" pillar.

**Delivered so far:** OIDC verifier + JWKS cache + license-gated `New()` factory in `internal/auth/sso/` (PR #114). Zero-dep stdlib crypto (no `go-oidc/v3`) so the airgapped-deploy story stays clean. Supports RS256/384/512 + ES256/384; refuses HS* + `none`. `TransportMapping` claims resolve to `Identity.TransportIDs` at verify time.

**Follow-ups:** (a) SAML backend via `crewjam/saml` alongside OIDC; (b) directory-sync source (SCIM 2.0 pull or IdP-native API) so `ResolveTransportID` returns real answers instead of `ErrNotFound`; ~~(c) daemon-assembly wiring so transports actually call `Directory.VerifyToken`~~ — **delivered in PR #122** (`/login <token>` + `/logout` chat commands, per-transport `sso.BindingStore` in SQLite, Router `allowed()` relaxation for SSO-verified senders, licence-gated daemon assembly, doctor `identity.sso.*` rows; fail-CLOSED discipline on store errors so an OIDC hiccup can't leak the allowlist).

**Estimate:** OIDC pilot shipped in 1 day. Daemon wiring shipped in 1 day. SAML + directory-sync ≈ 2–3 days.

### 2.3 `rousseau doctor` reports licence status ⚖ **core** (but structural for paid)

Extend `rousseau doctor` to print `identity.license.*` rows (tier, subject, features, expires_at). Reads from `license.Checker.Info()` — no separate storage. Never prints the raw token.

**Delivered (PR #115):** rows emitted directly after `build.go` — `identity.license.tier` always present; `subject` / `features` / `expires_at` only when a licence is loaded. Core tier renders as one bare info row; a valid paid tier renders `ok` (tier), plus rows for subject, comma-separated features, and RFC3339 expiry + human delta ("in 168d"). Inside the 14-day warn window the tier row + expiry row both flip to `warn` and the expiry detail appends "— renew soon". Cryptographic / structural failures render the tier row as `fail` with the reason parenthesised. A defensive test asserts the raw token can never appear in the rendered output.

**Why this is core:** paying customers need this to debug "my licence didn't activate" without grepping journalctl. OSS operators see a "tier: core" row that quietly demonstrates the paid tier exists (top-of-funnel awareness with zero friction).

**Estimate:** 1 day (shipped).

### 2.4 Helm chart + HA database backend 🎯 **core** (enterprise ergonomics)

Two deliverables in one workstream because they only matter together:

- **Helm chart** under `deploy/helm/` — the deploy target for every prospective paying customer with a k8s cluster (i.e. all of them).
- **Postgres backend for `state.Store`** — abstracts the current SQLite-hardcoded store behind a driver interface so multiple daemon replicas can share session state. Redis for the session-cache tier as a follow-up.

Ships free — deployment ergonomics belong in the core because every enterprise trial starts with "can I deploy this in my cluster?". A hard-to-deploy OSS product has no paid conversion funnel.

**Delivered so far (PR #116):** `internal/state/postgres/` implements the canonical `state.Store` (Save/Load/List/Delete/Close) on top of pgx v5. `StateConfig` gains `driver` + `dsn` fields; empty driver defaults to sqlite so existing single-replica installs are byte-compatible. `openStore` dispatches on driver; extension-hungry commands (mcp, session, daemon) now go through `openSQLiteStore` which errors cleanly if the operator has selected postgres — prevents a silent HA regression where a "postgres-configured" deploy quietly falls back to per-replica SQLite for cron/jidmap/oauth. `rousseau doctor` surfaces `state.driver` + a redacted `state.dsn`.

**Follow-ups:**
- Port the extension tables (cron, jidmap, oauth, recall, session_cache, session_costs) to Postgres so the whole daemon (not just sessions) is HA — one PR per table so each keeps a small review surface.
- ~~Helm chart under `deploy/helm/`~~ — **delivered in PR #121** (chart at `deploy/helm/rousseau-agent`). Values.yaml with commented defaults, Deployment / Service / ConfigMap / Secret / PVC / ServiceAccount / ServiceMonitor templates, NOTES.txt with licence + multi-replica warnings. `helm lint --strict` clean. No Postgres subchart dependency — enterprises bring their own DSN.
- Redis session-cache adapter for read-hot session lookups.

**Estimate:** Postgres pilot shipped in 1 day. Extension ports ≈ 3 days. Helm chart shipped in 1 day. Redis cache ≈ 1 day.

---

### 2.5 Reframe `internal/tenant` from SaaS to workspaces ⚖ **core** (cleanup)

**Delivered (PR #119):** package renamed `internal/tenant` → `internal/workspace`; all types (`ID`, `Config`, `Resolver`, `Registry`, `NewMapResolver`, `WithID`, `FromContext`) keep their shape but reframed via package doc, error messages, ctx-key names, and comments to name what it actually is: **logical workspaces / teams within a single on-premise deployment**. `docs/multi-tenant.md` replaced by `docs/workspaces.md` with a fresh "what a workspace is (and is not)" section that makes the non-SaaS boundary explicit. COMMERCIAL.md + README.md updated to match. 100 % test coverage retained. No behaviour change; the runtime is byte-identical.

**Why this mattered:** stopped the confusion. A future contributor reading `internal/tenant` was one wrong assumption away from adding a `tenant_id` column to every table + building SaaS-shaped features on top of it. The rename + doc rewrite closes that trapdoor.

**Estimate:** 0.5 days (shipped).

### 2.6 Google Vertex + AWS Bedrock providers ⚖ **core**

Deferred from §1.10 multi-provider registry. Bedrock already shipped (`internal/llm/bedrock`); Vertex is symmetric. Both use SigV4 / OAuth flows the OpenAI-compatible shape does not cover. Strengthens the "any LLM provider" OSS pitch — top-of-funnel adoption for regulated shops that mandate their cloud's managed-Claude.

**Estimate:** 2 days each (Vertex ≈ 2, Bedrock hardening ≈ 1). Low priority; OpenRouter already serves both behind an OpenAI-compatible façade.

### 2.7 libsignal-net Go bindings ⚖ **core**

Removes the JVM dependency from the Signal transport (currently shells out to `signal-cli`). Waiting on upstream libsignal-net Go stability.

**Estimate:** 3–5 days once bindings are stable; skip until then.

---

### 2.8 Signed / verified skills bundle 🔒 **paid** (was: skill marketplace)

**Boundary:** would extend `FeatureGovernanceAdvanced` (see [`docs/COMMERCIAL.md`](COMMERCIAL.md) §2.3).

**Recut from the old §2.3 "skill marketplace".** The marketplace concept as originally framed had commercial gravity but no monetization path if it stayed OSS-only. The re-framed version: agent-authored skills remain a **core** feature (they're a competitive moat for the OSS product), but the **signed / verified skills bundle** — cryptographically-verified skill packages with vendor attestation, an SBOM per skill, and centralised skill-catalogue management — becomes an enterprise feature. Compliance officers pay to know exactly which skill the model just triggered, from whom, and whether it's been tampered with.

**Delivered (PR #131):** `internal/skills/bundle` ships the `.skill.json` bundle format — one JSON per skill carrying a manifest (name/version/publisher/published_at/triggers), the skill content, an optional CycloneDX SBOM, and an Ed25519 publisher signature. `bundle.Verify(trustedKeys)` performs the full trust chain: algorithm=ed25519, publisher key in the operator's trust list, content hash matches, SBOM hash matches, Ed25519 verify over the manifest-hash. `internal/skills.LoadBundles(dir, opts)` scans `*.skill.json` files, verifies each, drops unverified silently (WARN or ERROR log per Strict flag). Daemon `buildSkillsProvider` composes verified bundles with plain markdown skills; three-condition gate matches the RBAC / OPA pattern (no config / no licence / no trusted keys → bundles ignored, plain skills untouched). Doctor rows `identity.governance.skill_bundles.{dir, trusted_publishers, licensed}`.

**Follow-ups:** publisher-side signing tool (`rousseau skills sign <manifest.json> --key <priv>`) to make bundle creation ergonomic for CI pipelines. The `bundle.Sign` primitive already exists in-package; a CLI wrapper is a small standalone PR.

**Estimate:** ~1 week end-to-end (per original ROADMAP); shipped in 1 day.

### 2.9 Advanced governance ecosystem (OPA + multi-party approvals) 🔒 **paid**

**Boundary:** `FeatureGovernanceAdvanced` (see [`docs/COMMERCIAL.md`](COMMERCIAL.md) §2.3).

RBAC with role hierarchies inherited from SSO groups, [Open Policy Agent](https://www.openpolicyagent.org/) integration for Rego-based tool-call policy, and multi-party approval workflows (e.g. "`terraform apply` needs a DevOps rota approval in Slack"). Plugs into the existing `agent.Approver` interface so the seam is clean; the OSS `PatternApprover` + TUI interactive approver stay unchanged.

**Delivered so far (PR #123):** first slice — group-based RBAC. `internal/agent/rbac` ships `Approver` (wraps inner approver, gates by `sso.Identity.Groups`, fail-CLOSED on anonymous requests). Config surface `agent.approver.rbac.rules` (tool → allowed_groups). Licence gate at daemon assembly (rules without licence → INFO log + inner approver returned unchanged; broken config → WARN + inner unchanged so a bad rule can't take the daemon offline). Router now stashes the verified `sso.Identity` into ctx after SSO lookup so the approver reads WHO's making the request. Doctor rows `identity.governance.rbac.*` (rule count + licensed status). 100 % coverage on the new package.

**Delivered (PR #129):** OPA slice — `internal/agent/opa` ships a Rego-per-tool-call approver mirroring the RBAC wrapping pattern. Config `agent.approver.opa.{policy_file, query}`; daemon composition `inner ← RBAC ← OPA` so a tool call must pass both governance layers. Rego input document carries `{tool, input (parsed JSON), session_id, actor, groups, email}` — policies gate on any combination. Standard v1 Rego syntax with `import rego.v1`. Fail-CLOSED discipline: bad policy → WARN + inner returned (never take daemon offline); ctx cancel → deny (explicit gate at start of Approve, not race with Rego's own ctx check); result-not-an-object → deny; anonymous callers see `actor=""` + `groups=[]` so policies can require identity. Doctor rows `identity.governance.opa.{policy_file, licensed}` with fail status when policy file is missing.

**Delivered (PR #130):** multi-party approvals — `internal/agent/approval` ships a `PendingManager` + `Approver` that blocks tool calls until N distinct SSO-authenticated approvers reply `/approve <token>` (or `/deny <token>` short-circuits). Router grows `/approve` and `/deny` chat commands, gated on the pending manager being non-nil. Config `agent.approver.multi_party.rules` (tool → NeededApprovals + timeout). Composition `inner ← RBAC ← OPA ← MultiParty` — the multi-party layer is outermost. Fail-CLOSED: anonymous requesters denied immediately (multi-party is meaningless without an authenticated identity to hold accountable); self-approve rejected AND audited (security-interesting event even though not counted); timeout / ctx-cancel → deny; unknown token → legible chat reply. Full lifecycle audit trail: `approval.request` / `approval.approve` / `approval.deny` / `approval.resolve` records in the tamper-evident hash chain. Doctor rows `identity.governance.multi_party.{rules, licensed}`. In-memory PendingManager (pilot); cross-restart / cross-daemon replication deferred to a small follow-up on the same interface.

**Estimate:** RBAC + OPA + multi-party slices shipped in 1 day each — §2.9 governance-advanced now complete.

### 2.10 Enterprise redaction rule packs 🔒 **paid**

**Boundary:** `FeatureAuditEgress` (see [`docs/COMMERCIAL.md`](COMMERCIAL.md) §2.2).

Industry preset rule packs for `internal/observability/redact` (HIPAA, PCI-DSS, GDPR) plus a rule-authoring surface (YAML → compiled matcher). The baseline redact rules (credentials, common API-key shapes) stay in the core.

**Estimate:** 3 days per rule pack; deferred until a customer names their compliance regime.

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
