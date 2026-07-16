# Why not just use OpenClaw?

**TL;DR:** OpenClaw is the right answer if you want the widest
possible messaging surface out of the box (29+ transports), a
skill marketplace with agent self-extension, and the DX of a
TypeScript project you install with pnpm. rousseau-agent is the
right answer if you want a single static Go binary, container-native
deployment, provenance-verifiable releases, and an MCP server
surface.

They're both "personal AI daemon you run on your own hardware,"
but they've made different bets on packaging, runtime, and how tools
enter the system.

## What OpenClaw is

[OpenClaw](https://openclaw.ai) is an open-source, local-first
personal AI assistant. Installs with `pnpm install` and runs on
macOS, Windows, and Linux. Distinctive features:

- **29+ communication platforms**: WhatsApp, Telegram, Discord,
  Slack, iMessage, Signal, plus 23 more that stretch across regional
  chat networks (WeChat, Line, KakaoTalk, VK, Viber, and others).
- **Self-extensibility**: the assistant writes its own skills at the
  operator's request. Skills persist and can be reviewed.
- **ClawHub**: a community skill marketplace with SkillSpector
  scanning that flags dangerous permissions before installation.
- **Microsoft Execution Containers**: Windows sandboxing for skill
  execution.
- Multi-provider LLM support (Claude, GPT, MiniMax 2.5, and local
  models).

Free and open source. TypeScript stack, pnpm-managed.

## Where OpenClaw wins

**Transport breadth.** 29+ vs rousseau's 9. If your reach depends on
WeChat, Line, KakaoTalk, VK, Viber, or another regional chat network,
OpenClaw is the shortest path. rousseau prioritises the eight
transports enterprise operators actually reach for (WhatsApp, Signal,
Slack, Discord, Matrix, Telegram, Email, SMS, iMessage) and treats the
long tail as "add when someone asks."

**Skill marketplace.** ClawHub is a real community of skill authors
publishing installable capabilities. SkillSpector's static permissions
scan is a genuinely good security story for a "click-to-install"
model. rousseau's skills package (`internal/skills/`) supports
first-party skills but there's no marketplace and no skill exchange.

**Agent-authored skills.** OpenClaw lets the assistant write its own
skills at operator instruction, then persist them. This is a
capability rousseau has explicitly deferred (§12 of the implementation
plan) because a marketplace + skill exchange + safe sandbox story
must be built first.

**Cross-OS install ergonomics.** `pnpm install openclaw` and you're
running on macOS/Windows/Linux. rousseau's install path is
`podman run` or `go install github.com/sebastienrousseau/rousseau-agent`;
the container is fine on macOS/Windows via Podman Desktop, but "run
this npm package" is a lower activation cost.

**Windows sandboxing.** Microsoft Execution Containers are a first-
party Windows story for skill isolation. rousseau's sandboxing story
is Linux-centric (rootless container + seccomp + drop-caps) and
weaker on Windows.

## Where rousseau wins

**Single static binary.** rousseau compiles to a ~20 MB static Go
binary with `CGO_ENABLED=0`. No Node.js runtime, no pnpm, no npm
lockfile, no `node_modules/` tree. `strace` the running daemon and
you see exactly what it does. OpenClaw's TypeScript stack is fine
day-to-day but auditors do prefer a single binary.

**Provenance-verifiable releases.** Every rousseau release ships
with SLSA-3 provenance, cosign-signed archive checksums, CycloneDX
SBOM per architecture, and a reproducible-build CI gate that fails a
release whose bytes don't match a fresh rebuild. OpenClaw ships none
of these today.

**Container hardening.** rootless Podman container with drop-all-caps,
seccomp, read-only rootfs, `UserNS=keep-id`, and a documented
egress-allowlist example. OpenClaw's runtime target is the operator's
laptop; the container hardening story isn't a first-party product
concern.

**MCP server surface.** rousseau exposes its own state (sessions,
cron jobs, allowlists, jid-map) as an MCP server. Claude Code,
Cursor, and every future MCP host can read from and act on that
state without a language-specific SDK. OpenClaw does not currently
speak MCP.

**Structured output helper.** rousseau's `internal/agent` package
exposes a typed JSON-schema-driven output helper that guarantees the
model returns valid JSON matching a caller-supplied schema. Useful
for building tool-driven flows without a downstream re-parse.
OpenClaw's assistant-emits-text pattern is more informal.

**Fuzz + property tests + benchmarks on load-bearing paths.**
Every wire parser has both a Go native fuzz target (`go test -fuzz`)
and a `testing/quick` property test. `internal/agent/compressor.go`,
`internal/transport/*/handleFrame`, and the message-parsing helpers
all have benchmarks. OpenClaw's public repo doesn't cite fuzz or
benchmark discipline.

**Reproducible build.** The reproducible-build CI job compiles the
tree twice on the same runner (Go 1.26, `GOTOOLCHAIN=local`,
`-buildvcs=false`) and diffs the SHA-256 of every artefact. Anyone
can reproduce the release binary locally with the documented
`SOURCE_DATE_EPOCH` + `-trimpath` + `-buildid=` invocation. OpenClaw
does not ship a reproducible build.

**100% godoc on exports** enforced by revive in CI. Every exported
identifier has a doc comment starting with the identifier name. Not
a marketing badge — it's a hard gate on merges.

**Native MCP server + a set of built-in tools** (read, write, edit,
grep, bash) with the same JSON-schema pattern. Plus (as of Week 2)
26 more native integrations across GitHub / Slack / Google
Workspace / Linear / Stripe.

**Voice-note transcription** on WhatsApp via whisper.cpp — inbound
voice messages arrive as text in the model's context.

## The scenario, laid out on both

**Job:** wire an agent that (1) triages WhatsApp inbound, (2) posts
digests to a family Signal group, (3) files GitHub issues for anything
that needs follow-up, (4) runs inside a rootless container on your
home server so nothing lives on your laptop.

**OpenClaw path**

1. `pnpm install` on your home server (installs Node.js + pnpm if not
   already there).
2. `openclaw init` — guided config wizard.
3. Pair WhatsApp + Signal transports. Install the GitHub skill from
   ClawHub.
4. Run `openclaw daemon` under whatever supervisor you prefer (systemd
   user unit, tmux, etc.).
5. Container packaging is left to you — no first-party Dockerfile.

**rousseau path**

1. `podman pull ghcr.io/sebastienrousseau/rousseau-agent:latest` on your
   home server.
2. `podman run --rm -v $HOME/.config/rousseau:/etc/rousseau
      rousseau-agent init` to generate a config.
3. `podman run` the daemon (Quadlet unit shipped in
   `docker/rousseau-agent.container`; systemd runs it rootless).
4. `rousseau whatsapp --allow <your-jid>` — QR pair.
5. WhatsApp / Signal transports and the `github_create_issue` tool
   are wired via config; the daemon starts, the model is reachable.

Both work. OpenClaw is closer to "npm install and go"; rousseau is
closer to "container image, systemd unit, one file config, done."

## Score-card view

Reproduced from `docs/COMPETITORS_2026_07_12.md`, updated to reflect
Week-1 (hardening) and Week-2 (26 native integrations) work:

| Axis | rousseau | OpenClaw |
| --- | :-: | :-: |
| Transport count | 9 | 29+ |
| Single static binary | ✅ | ❌ (TypeScript) |
| Rootless container | ✅ | ❌ |
| SLSA-3 provenance | ✅ | ❌ |
| Cosign-signed releases | ✅ | ❌ |
| CycloneDX SBOM | ✅ | ❌ |
| Reproducible build in CI | ✅ | ❌ |
| MCP server surface | ✅ | ❌ |
| Skill marketplace | ❌ | ✅ (ClawHub) |
| Agent-authored skills | ❌ | ✅ |
| Fuzz + property tests | ✅ | ❌ |
| Voice-note transcription | ✅ (WhatsApp) | 🟡 |
| Native integrations (Google/GitHub/Slack/Linear/Stripe) | ✅ | ❌ |
| Structured output helper | ✅ | 🟡 |

## Who should pick which

**Pick OpenClaw if…**

- You need one of the 20+ regional chat transports rousseau doesn't
  ship (WeChat, Line, KakaoTalk, VK, Viber…).
- You want a click-to-install skill marketplace.
- You want the assistant to author its own skills for you.
- You're on Windows and want first-party sandboxing.
- Your operator team is more comfortable maintaining a TypeScript
  stack than a Go binary + container.

**Pick rousseau if…**

- Your security review expects SLSA-3, SBOM, cosign, and a reproducible
  build.
- You want a single static binary in a rootless hardened container.
- You want your daemon to speak MCP as a first-class server so Claude
  Code / Cursor / other MCP hosts can plug into your state.
- Nine transports cover your users (they usually do — the 20 regional
  transports OpenClaw adds are important, but only for specific
  markets).
- You want native GitHub / Slack / Gmail / Calendar / Drive / Linear /
  Stripe tools already wired in the daemon.
- You value reproducible builds, fuzz targets, property tests, and
  benchmarks on wire parsers as a hard gate on merges.

## Direct interop

OpenClaw and rousseau don't currently share a wire protocol. Both
implement MCP client behaviour (rousseau via the `claude` CLI's
built-in MCP client when using the default provider), but neither
consumes the other's state today.

If you already run OpenClaw and want to switch, rousseau's config
schema is documented in `internal/config/config.go`; every session is
JSON-serialisable and can be imported into rousseau's SQLite store
with a small migration script. Reach out via GitHub Issues if you
want that path documented in detail.
