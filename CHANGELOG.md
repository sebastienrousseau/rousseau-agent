# Changelog

All notable changes to `rousseau-agent` are documented here.

Versioning follows the project convention: every release increments by
exactly `+0.0.1`. `v0.0.1` is the first tag; `v0.1.0` only follows
`v0.0.999`; `v1.0.0` only follows `v0.999.999`. The version number
carries no semantic-version meaning — breaking-change intent is
signalled in the release-notes entry, not in the version string.

The format is loosely based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); commit
messages follow Conventional Commits (`feat:`, `fix:`, `refactor:`,
`docs:`, `test:`, `chore:`, `ci:`).

## [Unreleased]

Ships in `v0.0.2` alongside the roadmap Wave 1-3 delivery.

### Added — Wave 1 (unblock credibility)

- **MCP client** (`internal/mcp/client`) — consume tools from
  external MCP servers (github, playwright, postgres, filesystem, …)
  by declaring them under `mcp.clients` in `config.yaml`. Stdio
  transport shipped; SSE / HTTP follow-ups.
- **spawn_subagent tool** — registers the existing
  `subagent.Spawn` primitive as a model-callable tool with a
  structured JSON summary output.
- **Container-release workflow** (`.github/workflows/container-
  release.yml`) — publishes both `full` and `distroless` images to
  ghcr.io on tag, cosign-signed with SLSA build attestations.
- **Benchmark harness** (`test/benchmarks/`) — SWE-Bench Verified,
  Aider Polyglot, and Terminal-Bench runners behind `//go:build
  bench`; weekly CI workflow uploads JSON artefacts.
- **`docs/compatibility.md`** — pkg/ vs internal/ stability
  contract; version-bump-neutral (per project convention every
  release increments by +0.0.1).

### Added — Wave 2 (Hermes-parity)

- **Voice-note transcription** (`internal/media/audio`) — Whisper.cpp
  local backend + OpenAI Whisper API fallback + Noop for tests.
  Wired end-to-end into WhatsApp (baseline), Telegram, and Discord —
  audio-only messages route through the transcriber and are delivered
  to the handler as normal text. Signal / iMessage / Matrix carry the
  same `Transcriber` config surface (uniform operator knob) with
  per-protocol audio-detection landing in v0.0.3.
- **Identity resolver** (`internal/identity` +
  `internal/state/sqlite/identity.go`) — maps
  `<transport>:<sender>` pairs to a stable identity so a
  conversation can span WhatsApp → Slack → email. Chat commands
  (`/whoami`, `/link`) in a follow-up.
- **Per-session cost telemetry** (`internal/pricing` +
  `internal/state/sqlite/session_costs.go` + `internal/state/sqlite/
  cost_recorder.go`) — records every completion's usage + estimated
  USD cost; `rousseau session cost [session-id]` CLI (with
  `--group-by`, `--since`, `--json`).
- **Prompt-cache instrumentation** — Anthropic adapter now sets
  1-hour TTL on system+tools and 5-minute TTL on messages (matches
  2026 caching guidance); `rousseau_prompt_cache_tokens_total`
  Prometheus counter split by type + TTL bucket for hit-ratio
  charts.

### Added — Wave 3 (differentiators)

- **Multi-model routing** (`internal/llm/router`) — new
  `provider: router` selects a child provider per request based on
  configurable rules (message length, tool-use count, session-id
  prefix). Emits `rousseau_router_decisions_total` metric.
- **Lifecycle hooks** (`internal/agent/hooks`) — external scripts
  fire at `pre_tool_use` / `post_tool_use` / `pre_turn` /
  `post_turn` / `on_error`. Deny verdict blocks the operation with
  a synthetic error surfaced to the model. Fail-open on hook errors.
- **Signed skills** (`internal/skills/verify.go`) —
  `SSHKeygenVerifier` verifies skill files via `ssh-keygen -Y
  verify` against an OpenSSH allowed-signers file. Strict mode
  drops unsigned skills; non-strict logs a WARN.
- **Bundled skills** (`skills/`) — starter set (`git-rebase`,
  `review-diff`, `whatsapp-transcript-summary`, `podman-quadlet`)
  copied into the container image at `/etc/rousseau/skills/`;
  user-drops under `~/.local/share/rousseau/skills/` still win.
- **Sandbox scaffold** (`internal/tools/sandbox` +
  `docs/security/sandbox.md`) — `none` backend fully shipped;
  `gvisor` / `nsjail` scaffolds wire the argv but need runtime
  binaries on PATH; `firecracker` scaffold-only.
- **A2A protocol runtime** (`internal/a2a`, `internal/a2a/server`,
  `internal/a2a/client`, `docs/a2a.md`, `examples/embed-a2a`) —
  HTTP/JSON server with SSE task streaming
  (`GET /.well-known/agent-capabilities`, `POST /tasks`,
  `GET /tasks/{id}`, `GET /tasks/{id}/events`,
  `POST /tasks/{id}/cancel`), bearer-token auth allowlist, in-memory
  task store with bounded history-replay so late SSE subscribers
  don't lose events, plus a matching client that `SubmitTask` →
  drains updates on a channel until terminal Status.

### Added — Wave 4 (moonshot scaffolds)

- **Letta-style memory runtime** (`internal/memory/letta` +
  `docs/memory-letta.md`) — in-memory `Store` (`NewMemoryStore`)
  implementing byte-budgeted core memory with auto-demotion of the
  oldest facts into substring-ranked archival memory on `WriteCore`.
  Persistent SQLite/vector backend still deferred (`NewSQLiteStore`
  returns `ErrScaffold`).
- **Plan-mode runtime** (`internal/agent/plan` +
  `docs/plan-mode.md`) — `Executor.Run` / `Rewind(n)` / `Resume`
  driving a Plan step-by-step with per-step approval gates and
  checkpoint recording. `MemoryCheckpointStore` ships as the
  default backend; SQLite persistence + the `/plan` chat command
  are follow-ups.
- **Workspace resolver runtime** (originally `internal/tenant` +
  `docs/multi-tenant.md`; renamed in v0.0.3 to `internal/workspace` +
  `docs/workspaces.md` — see ROADMAP §2.5) — `NewMapResolver([]Config)` →
  `Registry` with three allowlist patterns (exact
  `<transport>:<sender>`, transport-agnostic `<sender>`, catch-all
  `*`) + `ConfigFor(id)` / `All()` accessors for downstream
  per-workspace credentials + approver rules.

### Coverage

- `pkg/*` façade packages moved from 0% test coverage to 91.7%.
- Every new package ships with race-clean, `vet`-clean tests.

### Fixes

- `slsa.yml` example-verification comment referenced `v0.1.0` (a
  version that only comes after v0.0.999 per the project's
  monotonic +0.0.1 policy); corrected to `v0.0.1`.

### Housekeeping

- `.gitignore` excludes `test/benchmarks/results/`.
- `docs/compatibility.md` documents the stability contract for
  `pkg/`, CLI flags, config schema, container tags, MCP protocol,
  providers, transports, metrics, and on-disk formats.

## [v0.0.1] — first tagged release

Marks the first cut of `rousseau-agent` with a versioned, attested
release artefact. Every capability listed here already existed on
`main`; this tag freezes it into a downloadable, verifiable bundle.

### Provider surface (LLM)

- Anthropic Messages API, with prompt-cache breakpoints on system
  prompt + tools (1-hour TTL) and last message (5-minute TTL)
- OpenAI Chat Completions (also drives OpenRouter and Ollama presets)
- Google Vertex AI with OAuth2 (service account or ADC)
- AWS Bedrock (Anthropic and Meta model families)
- Local `claude` CLI (`claudecli`) — inherits your Claude Code auth
  without plumbing API keys; opt-in `--bare` mode via
  `ROUSSEAU_CLAUDECLI_BARE=1` cuts cold-start when the mounted
  workspace is large

### Transports (chat channels)

- WhatsApp (via `whatsmeow`, incl. LID → PN self-chat substitution)
- Slack (Bolt + Socket Mode)
- Discord
- Telegram
- Signal (via `signal-cli`)
- Matrix (Synapse-compatible)
- SMS (Twilio, Vonage)
- iMessage (macOS-only)
- Email (IMAP + SMTP)

Each transport plugs into the same allowlist-first router; a single
daemon can run any single transport, and the same binary handles all
nine.

### Agent loop

- `internal/agent/agent.go` `Turn` orchestrates compression → recall
  system-prompt appendix → provider `Complete` → tool dispatch with
  approver gate → loop until end-of-turn
- Sub-agent primitive (`internal/agent/subagent`) available as a Go API
  (surfacing to the loop as a tool ships in a later release — see
  ROADMAP)
- Compression via `internal/agent/compressor` collapses long sessions
  while preserving credentials, TODOs, and cached prefixes
- Recall via SQLite FTS5 + hybrid vector search (`internal/recall`);
  configurable embedder (`voyage-ai` or the `noop` deterministic
  stub for tests)

### Tools

- Built-in filesystem set: `bash`, `read`, `write`, `edit`, `grep`
- Native integrations: GitHub, Slack, Google (Gmail/Calendar/Drive),
  Linear, Stripe
- Composio meta-integration adapter (registers every action the
  authenticated user has on Composio)

### Skills

- YAML-front-matter Markdown skills loader; skills with matched
  triggers get spliced into the system prompt as `## Skill Name`
  sections
- Skills directory resolves in this order: `--skills-dir` flag,
  `$ROUSSEAU_SKILLS_DIR`, `~/.local/share/rousseau/skills/`

### Scheduler

- `robfig/cron`-backed job runner with SQLite persistence
- Configurable poll interval (60 s default), delivery hook to any
  transport, Prometheus `rousseau_cron_fires_total{job,status}`

### OAuth broker

- Concurrent-flow-safe OAuth2 broker (`internal/auth/oauth/broker.go`)
- AEAD-encrypted token vault (SQLite-backed)
- Google, Linear, Composio flows

### MCP

- Server mode (`internal/mcp/server`) — rousseau exposes its tool
  registry as an MCP endpoint for external clients (Claude Desktop,
  Cursor, etc.)
- Client mode ships in v0.0.2 (see ROADMAP W1.3)

### Observability

- `log/slog`-only, dotted event names (`agent.turn`,
  `whatsapp.incoming`), redaction middleware for auth tokens
- Prometheus metrics: provider latency + errors, transport in/out,
  cron fires, tool calls, compressor rewrites, session gauge, panics
  recovered
- OpenTelemetry tracing via OTLP-HTTP (noop when no endpoint set)

### Supply-chain

- SLSA Level 3 provenance via `slsa-github-generator` reusable
  workflow — attests every artefact against a GitHub OIDC identity
- CycloneDX SBOM per archive (Syft-generated)
- Cosign-signed checksums file
- Reproducible bit-identical builds verified in CI
  (`reproducible-build.yml`)
- Cross-platform binaries in one release: Linux amd64/arm64/armv6/
  armv7/riscv64, macOS amd64/arm64, Windows amd64
- Two-flavour binaries: `rousseau` (full, all transports) and
  `rousseau-lite` (`-tags no_whatsmeow`, ~14 % smaller for operators
  who don't need WhatsApp)

### Container

- Two Dockerfile flavours:
  - `docker/Dockerfile` — full agent runtime with bash/git/python/
    node/mise/chezmoi for coding-tool use inside the container
  - `docker/Dockerfile.distroless` — minimal runtime for
    daemon-only deployments (WhatsApp bridge, cron scheduler, etc.)
- Rootless Podman with dropped capabilities, read-only rootfs,
  seccomp profile, UID/GID keep-id

### Quality gates

- 83.5 % test coverage across 159 test files, race detector on Linux
  and macOS
- `golangci-lint` v2 with 18 linters, `forbidigo` blocks `fmt.Print*`
  outside `main` and tests
- Fuzz tests on MCP protocol parsing and every transport that reads
  external bytes (WhatsApp, Discord, Email, Slack)
- `govulncheck` blocking on CVEs reaching imported symbols

## Verifying a release

```bash
# Grab the release + attestation
gh release download v0.0.1 -R sebastienrousseau/rousseau-agent \
    -p 'rousseau_v0.0.1_linux_amd64.tar.gz' \
    -p 'checksums.txt' \
    -p 'checksums.txt.sig' \
    -p 'multiple.intoto.jsonl'

# Verify the SLSA provenance (source-tag must match the release tag)
slsa-verifier verify-artifact \
    --provenance-path multiple.intoto.jsonl \
    --source-uri github.com/sebastienrousseau/rousseau-agent \
    --source-tag v0.0.1 \
    rousseau_v0.0.1_linux_amd64.tar.gz

# Verify cosign signature on the checksums
cosign verify-blob \
    --certificate-identity-regexp 'https://github.com/sebastienrousseau/rousseau-agent/.*' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --signature checksums.txt.sig \
    checksums.txt
```

[Unreleased]: https://github.com/sebastienrousseau/rousseau-agent/compare/v0.0.1...HEAD
[v0.0.1]: https://github.com/sebastienrousseau/rousseau-agent/releases/tag/v0.0.1
