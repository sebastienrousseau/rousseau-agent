# Why not just use ZeroClaw?

**TL;DR:** ZeroClaw is the right answer if you're deploying to a
Raspberry Pi Zero, a $10 ARM board, or a RISC-V edge device — nothing
else in this space touches its 3.4 MB binary and <5 MB RAM footprint.
rousseau-agent is the right answer if you want the full set of
features (nine transports, cron scheduler, MCP server, cross-session
recall, LLM-summarised compression, native tool integrations) and can
spend the extra ~20 MB of binary + 100 MB of RAM.

Both are Rust-adjacent projects with a strong efficiency story;
ZeroClaw picked "smallest possible" as its axis and rousseau picked
"most complete self-hosted daemon" as its axis. They aren't really
competitors so much as different points on the same size / capability
curve.

## What ZeroClaw is

[ZeroClaw](https://zeroclaw.net) is a Rust rewrite explicitly
positioned as "a highly efficient alternative to OpenClaw." Numbers
they publish:

- **3.4 MB static binary**
- **<5 MB RAM footprint**
- **400× faster startup than the reference OpenClaw stack**
- Cross-compiles to ARM, x86, RISC-V native

Features they ship:

- Native WhatsApp + Telegram + webhook server via a gateway command
- OpenRouter, other API-keyed providers
- **Trait-based architecture** for component swappability
- Built-in memory engine (vector embeddings + keyword search)
- Security features: pairing requirement, workspace-scoped file
  access, command allowlist (git/npm/cargo), encrypted secrets at
  rest

Open source, self-hosted.

## Where ZeroClaw wins

**Binary size.** 3.4 MB vs rousseau's ~20 MB (`:lite` build) or 30 MB
(`:distroless` build). Neither is exactly "large" but if you're
shipping to 200 devices with 128 MB of flash, the difference matters.

**RAM footprint.** <5 MB steady-state vs rousseau's ~50 MB at rest
(dominated by the modernc.org/sqlite driver in pure-Go mode and by
whatsmeow's session state). On a Raspberry Pi Zero with 256 MB total,
this is the difference between "runs comfortably" and "runs with
swap."

**Startup latency.** ZeroClaw's Rust runtime starts in single-digit
milliseconds. rousseau's Go binary starts in ~50 ms. Neither is
human-perceptible interactively, but if you're rebooting a fleet of
edge devices ZeroClaw wins on cold-start recovery.

**Cross-arch reach.** ZeroClaw ships ARM, x86, and RISC-V builds. In
Week 3 (§3 of the implementation plan) rousseau shipped an amd64 +
arm64 `:lite` variant that gets close to the ZeroClaw size story, but
RISC-V remains a ZeroClaw niche.

**Rust type-safety guarantees.** The compiler prevents entire classes
of bug (data race, use-after-free, null-pointer) that Go's runtime
merely detects. In practice both languages ship reliable daemons, but
"we picked Rust" is a real signal for teams that care about that
axis.

## Where rousseau wins

**Transport breadth.** Nine transports (WhatsApp, Signal, Telegram,
Matrix, Slack, Discord, Email IMAP+SMTP, SMS via Twilio/Vonage,
iMessage via BlueBubbles) vs ZeroClaw's three (WhatsApp, Telegram,
webhook). If you need reach on Slack, Discord, Signal, Matrix, or
email, you either write those adapters yourself in Rust or pick
rousseau.

**MCP server surface.** rousseau exposes its own state as an MCP
server so Claude Code, Cursor, and any other MCP host can plug in.
ZeroClaw doesn't ship an MCP surface today.

**Cron scheduler.** rousseau ships a full crontab-driven scheduler
that fires prompts on a schedule, records every invocation in the
audit trail, and delivers the model's response back to a chosen
transport. ZeroClaw does not ship scheduled prompts today.

**LLM-summarised compression.** rousseau's `internal/agent/compressor`
runs a summariser on the older half of a conversation when the
message count crosses a threshold, keeping the context window
sustainable across long-running sessions. ZeroClaw's memory engine
does keyword recall but doesn't summarise historical turns.

**Cross-session recall.** rousseau's `internal/state/sqlite/search.go`
uses SQLite FTS5 to recall earlier sessions by keyword. Combined with
the compressor, this gives a model access to relevant history from
across sessions. ZeroClaw's memory engine is per-conversation.

**Provenance-verifiable releases.** SLSA-3 provenance, cosign-signed
archive checksums, CycloneDX SBOM per architecture, and a
reproducible-build CI gate. ZeroClaw ships none of these today.

**Container hardening.** rootless Podman + drop-all-caps + seccomp +
read-only rootfs + `UserNS=keep-id` + documented egress-allowlist.
ZeroClaw's runtime target is direct binary execution; hardening is
the operator's responsibility.

**Native tool integrations.** rousseau ships 26 native tools across
five suites (GitHub, Slack, Gmail/Calendar/Drive, Linear, Stripe
read-only) as of Week 2. ZeroClaw's tool surface is currently
smaller.

**Voice-note transcription.** WhatsApp voice notes are transcribed
via whisper.cpp and delivered as text into the model's context. Not
a ZeroClaw capability.

**Fuzz + property tests + benchmarks** on every wire parser.
ZeroClaw's tooling story is less public.

**100% godoc coverage on exports** enforced by revive in CI. Rust's
`cargo doc` is comparable, but ZeroClaw's docs coverage isn't publicly
audited the same way.

## The scenario, laid out on both

**Job:** run an agent on a Raspberry Pi Zero W (256 MB RAM, 4 GB
flash) that responds to WhatsApp inbound, forwards a daily summary
to a Telegram channel, and has no other requirements.

**ZeroClaw path**

1. Cross-compile the ZeroClaw binary for ARMv6 (Pi Zero is armhf).
2. Copy the 3.4 MB binary and a small config onto the device.
3. Systemd unit runs it as a normal user; <5 MB RAM steady-state.
4. Uses ~15 MB of storage total (binary + config + state DB).

**rousseau path**

1. Cross-compile the `:lite` build (`GOOS=linux GOARCH=arm GOARM=6`).
   Result: ~20 MB binary.
2. Copy binary + config; the SQLite state DB grows to a few hundred
   KB over time.
3. Systemd unit runs it as a normal user; ~50 MB RAM steady-state.
4. Uses ~30-40 MB of storage total.

On a Pi Zero W both work. On a Pi Zero (512 MB RAM total, no W),
ZeroClaw uses ~1% of RAM and rousseau uses ~10%. Neither is a
problem in practice, but the delta matters if you're doing something
else on the device.

**Job (alternate):** same but you want cron-scheduled prompts, Slack
transport, and an MCP surface so Claude Code can hit the daemon.

- ZeroClaw: rebuild it yourself. None of those features ship today.
- rousseau: config change; done.

## Score-card view

Reproduced from `docs/COMPETITORS_2026_07_12.md`, updated to reflect
Week-1 (hardening) and Week-2 (26 native integrations) work:

| Axis | rousseau | ZeroClaw |
| --- | :-: | :-: |
| Binary size (release) | ~20 MB (`:lite`) | 3.4 MB |
| RAM footprint | ~50 MB | <5 MB |
| Cross-arch (armhf, RISC-V) | 🟡 (arm64 yes, armhf/riscv no) | ✅ |
| Transport count | 9 | 3 |
| MCP server surface | ✅ | ❌ |
| Cron scheduler | ✅ | ❌ |
| LLM compression | ✅ | ❌ |
| Cross-session recall (FTS5) | ✅ | 🟡 (keyword) |
| Vector recall | 🔜 (§9) | ✅ |
| SLSA-3 provenance | ✅ | ❌ |
| Cosign-signed releases | ✅ | ❌ |
| CycloneDX SBOM | ✅ | ❌ |
| Reproducible build in CI | ✅ | ❌ |
| Rootless hardened container | ✅ | ❌ |
| Native integrations (Google/GitHub/Slack/Linear/Stripe) | ✅ (26 tools) | ❌ |
| Voice-note transcription | ✅ | ❌ |
| Fuzz + property tests | ✅ | 🟡 |

## Who should pick which

**Pick ZeroClaw if…**

- You're deploying to an edge / IoT device with <100 MB RAM or <50 MB
  storage.
- You need RISC-V or armhf builds.
- You want the smallest possible attack surface (fewer features
  literally means fewer lines of code that could be exploited).
- The three transports ZeroClaw ships (WhatsApp, Telegram, webhook)
  cover your use case.

**Pick rousseau if…**

- You're deploying on a normal server, laptop, or Raspberry Pi 3+
  where 50 MB of RAM isn't a constraint.
- You need nine transports (or the "we'll add a tenth next quarter"
  option).
- You need cron scheduling, MCP server surface, LLM-summarised
  compression, or cross-session FTS5 recall.
- You want SLSA-3 provenance, cosign-signed releases, CycloneDX SBOM,
  and reproducible builds as hard gates on merges.
- You want the 26 native tool integrations (GitHub / Slack / Gmail /
  Calendar / Drive / Linear / Stripe) already wired.

## Not really competitors

ZeroClaw and rousseau optimise for different points on the same
curve. ZeroClaw is "smallest possible daemon that does the three
things most people need." rousseau is "most complete self-hosted
daemon that a small platform team can adopt with confidence." An
enterprise team probably picks rousseau; a home-lab tinkerer with a
shelf of Pi Zeros probably picks ZeroClaw; a team running both — a Pi
Zero for the always-on chime and a container on a NUC for the actual
work — is a perfectly reasonable answer too.

If we later add a `:tiny` variant that gets closer to ZeroClaw's
footprint at the cost of feature reach, it'll live under
`docker/Dockerfile.tiny` and be documented as an explicit trade-off.
Not on the roadmap today.
