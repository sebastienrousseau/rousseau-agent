<!-- markdownlint-disable MD033 MD041 -->

<p align="center">
  <a href="https://github.com/sebastienrousseau/rousseau-agent">
    <img src="docs/assets/logo.svg" alt="rousseau-agent" width="180" onerror="this.style.display='none'"/>
  </a>
</p>

<h1 align="center">rousseau-agent</h1>

<p align="center">
  <strong>The self-hosted, container-native coding agent for teams that can't send their code to a cloud endpoint.</strong>
</p>

<p align="center">
  Nine chat transports · five LLM providers · <strong>26 native tool integrations + Composio (1000+)</strong> · <strong>sub-agent parallelism</strong> · <strong>vector recall</strong> · MCP server · cron scheduler · skills loader · <strong>OAuth broker with AEAD vault</strong> · SLSA-3 provenance · SBOM · cosign-signed releases · rootless Podman with dropped capabilities · <strong>5-arch cross-compile (amd64, arm64, armv6, armv7, riscv64)</strong>.
</p>

<p align="center">
  <a href="https://github.com/sebastienrousseau/rousseau-agent/actions/workflows/ci.yml">
    <img src="https://img.shields.io/github/actions/workflow/status/sebastienrousseau/rousseau-agent/ci.yml?branch=main&label=CI&style=for-the-badge" alt="CI status"/>
  </a>
  <a href="https://github.com/sebastienrousseau/rousseau-agent/actions/workflows/slsa.yml">
    <img src="https://img.shields.io/badge/SLSA-Level%203-blueviolet?style=for-the-badge" alt="SLSA 3"/>
  </a>
  <a href="https://github.com/sebastienrousseau/rousseau-agent/actions/workflows/codeql.yml">
    <img src="https://img.shields.io/badge/CodeQL-enabled-brightgreen?style=for-the-badge&logo=github" alt="CodeQL"/>
  </a>
  <a href="https://pkg.go.dev/github.com/sebastienrousseau/rousseau-agent">
    <img src="https://img.shields.io/badge/pkg.go.dev-reference-informational?style=for-the-badge&logo=go" alt="Go reference"/>
  </a>
  <a href="https://github.com/sebastienrousseau/rousseau-agent/blob/main/LICENSE">
    <img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="License: MIT"/>
  </a>
  <img src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go 1.26+"/>
</p>

---

## Contents

- [Positioning](#positioning)
- [Capabilities](#capabilities)
- [Supported transports](#supported-transports)
- [Supported providers](#supported-providers)
- [Enterprise & supply-chain posture](#enterprise--supply-chain-posture)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Deployment](#deployment)
- [Configuration](#configuration)
- [Embedding the agent loop](#embedding-the-agent-loop)
- [Repository layout](#repository-layout)
- [Quality gates](#quality-gates)
- [Security & disclosure](#security--disclosure)
- [Comparison](#comparison)
- [Contributing](#contributing)
- [License](#license)

---

## Positioning

`rousseau-agent` is a coding assistant designed around one operational assumption: **the workspace, the auth material, and the model traffic never leave a machine the operator controls.** The daemon runs as a single static binary inside a rootless Podman container. The LLM either shells out to your local `claude` CLI (inheriting whatever auth Claude Code already holds) or hits a provider endpoint you have a contract with — Anthropic, AWS Bedrock, Google Vertex, or any OpenAI-compatible endpoint including self-hosted Ollama.

Nine chat transports let the same daemon reach engineers on the medium they already use — WhatsApp, iMessage, Signal, Telegram, Matrix, Slack, Discord, SMS (Twilio / Vonage), or plain IMAP + SMTP. All of them speak to the same tool registry, session store, and approval policy.

There is no SaaS control plane, no telemetry endpoint, no license server, no vendored broker. The only outbound traffic is the LLM call and the transport you enabled.

## Capabilities

| Layer | What's shipped |
|---|---|
| **Agent loop** | Multi-turn planner with structured tool-use, streaming responses, per-session context, LLM-backed session compression, FTS5-backed cross-session recall + **hybrid vector recall** (`internal/recall`, sqlite blob + cosine + weighted-blend rerank). |
| **Sub-agent parallelism** | `internal/agent/subagent.Spawn(ctx, parent, provider, tasks, policy)` runs N detached-copy tasks with bounded concurrency, per-task timeout, and aggregate token budget. Two aggregators (human-readable + JSON) ship. |
| **Tool registry** | Concurrency-safe registry with `read`, `write`, `edit`, `grep`, `bash` **plus 26 native integrations** across GitHub / Slack / Google Workspace (Gmail + Calendar + Drive) / Linear / Stripe + the **Composio adapter** (opt-in, 1000+ apps behind one auth). |
| **OAuth broker + vault** | `internal/auth/oauth` — provider-agnostic broker with XChaCha20-Poly1305 vault, master key from `$ROUSSEAU_TOKEN_KEY` / OS keyring / mode-0600 file. Callback server on `127.0.0.1:8765`. Rotate-key preserves plaintext. |
| **Rate limiting + resilience** | Per-JID token bucket (`internal/ratelimit`), panic-recovery middleware, circuit breakers on every provider (`internal/resilience`, sony/gobreaker/v2). |
| **Approval policies** | `allow_all`, `deny_all`, or `pattern` mode with per-tool allow / deny regex rules and a configurable default. Unattended daemons pick sensible defaults automatically. |
| **Session store** | Durable SQLite (`modernc.org/sqlite`, embedded, no libc coupling) with WAL journaling, `busy_timeout=15s`, WAL checkpoints on `Close`. |
| **MCP server** | JSON-RPC 2.0 over stdio, spec revision 2024-11-05. Exposes rousseau's tools + sessions to any MCP-compatible client (Claude Desktop, IDE extensions, other agents). |
| **Cron scheduler** | robfig/cron/v3 goroutine with durable job storage; sends scheduled messages through any registered transport. |
| **Skills loader** | agentskills.io-compatible Markdown + YAML frontmatter format. Skills are discovered from `skills.dir`, composed into the system prompt, and version-tracked. |
| **Multimodal input** | Every provider adapter (Anthropic, Bedrock, Vertex, OpenAI, claudecli) maps `ContentImage` to native wire shape. `internal/media` policy: 10 MiB / image, 40 MiB / turn, MIME sniffed from first 512 bytes. |
| **Observability** | Prometheus registry with 12 `rousseau_*` metrics (provider latency, panics recovered, circuit state, rate-limit denies, sub-agent spawns, …). OpenTelemetry OTLP-HTTP tracer. Redacting slog handler with default rules for every credential shape rousseau touches. |
| **TUI** | Bubble Tea client with viewport, scrollback, streaming indicator, and typing feedback for chat transports. |
| **Container runtime** | Three tags (`:latest` 530 MB with claude CLI, `:distroless` 54 MB, `:lite` 47 MB with WhatsApp compiled out). Rootless Podman + systemd Quadlet unit. Read-only rootfs, all capabilities dropped, `NoNewPrivileges`, seccomp filter, non-root user, `keep-id` UID mapping. |
| **Cross-arch** | Compile-verified on every push: linux/{amd64, arm64, armv6, armv7, riscv64} × {default, lite} + darwin/amd64/arm64 + windows/amd64. |

## Supported transports

| Transport | Inbound | Outbound | Backing library / protocol |
|---|:---:|:---:|---|
| WhatsApp | ✅ | ✅ | `go.mau.fi/whatsmeow` (Signal-protocol-compatible) |
| Signal | ✅ | ✅ | `signal-cli` JSON-RPC subprocess |
| Telegram | ✅ | ✅ | Bot API (long polling) |
| Matrix | ✅ | ✅ | Client-server API |
| Slack | ✅ | ✅ | Socket Mode (outbound WebSocket, no public webhook) |
| Discord | ✅ | ✅ | Gateway v10 (WebSocket + intents) |
| iMessage | ✅ | ✅ | BlueBubbles HTTP polling |
| Email | ✅ | ✅ | IMAP inbound + SMTP outbound |
| SMS | ❌ | ✅ | Twilio REST / Vonage REST |

Every transport is a thin adapter behind the same `transport.Transport` interface (`Start`, `Stop`, `Deliver`). Adding a tenth is a few hundred lines of adapter + tests; nothing in the agent core moves.

## Supported providers

| Provider | Auth model | Notes |
|---|---|---|
| **claudecli** (default) | Inherits `claude` CLI auth | No API keys plumbed through rousseau's config. Recommended for individual operators. |
| **anthropic** | `ANTHROPIC_API_KEY` | Direct API, exact-pinned SDK, prompt-cache markers on the last two messages. |
| **openai / openrouter / ollama** | Configurable | Any OpenAI-compatible endpoint. Ollama presets `base_url` to `http://localhost:11434/v1`. |
| **AWS Bedrock** | Standard AWS credential chain | Enterprise-managed Claude on AWS. |
| **Google Vertex AI** | GCP service-account JSON | Enterprise-managed Claude on GCP. |

The provider abstraction is `agent.Provider` and `agent.StreamingProvider`. Adding a sixth is a single `Chat` / `ChatStream` implementation.

## Enterprise & supply-chain posture

| Control | Implementation |
|---|---|
| Build provenance | **SLSA Level 3** via `slsa-framework/slsa-github-generator`. |
| Release signing | **cosign** signs checksums; consumers verify with the published public key. |
| Software bill of materials | **CycloneDX JSON** attached to every release. |
| Reproducible builds | Dedicated `reproducible-build` CI job verifies bit-identical output on fresh checkouts. |
| Vulnerability scanning | `govulncheck` on every CI run; Dependabot for `gomod` and `github-actions`. |
| Static analysis | golangci-lint v2 (18 linters) + CodeQL (Go). |
| Dependency pinning | Exact-version pins in `go.mod`; `go.sum` frozen. |
| Runtime hardening | Read-only rootfs, `DropCapability=all`, `NoNewPrivileges=true`, default seccomp profile, non-root UID 1000, `keep-id` user namespace mapping. |
| No inbound HTTP surface | Every transport that requires incoming messages uses outbound WebSocket (Slack Socket Mode, Discord Gateway) or polling. There is no HTTP server to expose. |
| Race-condition testing | `go test -race -count=1 -covermode=atomic ./...` on Linux and macOS matrices. |
| Fuzz corpus | 6 fuzz targets (slack, discord, email × 2, whatsapp, mcp); `make fuzz` runs the full battery. |
| Property tests | Every load-bearing parser has a `testing/quick`-based property (500 random inputs each). |
| Wall-clock soak | `test/integration/soak` — synthetic workload with 5 leak invariants (goroutines, alloc, FDs, GC pressure < 5%, heap-in-use ≤ 2×). Runs on push (10 min), PR (30 min), nightly (24 h). |
| Cross-arch build gate | 12 arch/tag combos verified on every push including RISC-V. |
| Image-size gate | `:distroless` ≤ 70 MB, `:lite` ≤ 60 MB — regressions fail the PR. |
| Coverage | Overall package-avg **88.7%**; every campaign-shipped package has runnable godoc `Example*` and benchmarks. |

Reachable trust roots: GitHub Actions OIDC (SLSA), Sigstore public transparency log (cosign), and pkg.go.dev (Go module checksum database).

## Installation

### Prerequisites

- Go **1.26+**
- One of the supported provider paths above (default `claudecli` inherits from your locally installed `claude` CLI).

### From source

```bash
git clone https://github.com/sebastienrousseau/rousseau-agent
cd rousseau-agent
make build             # produces ./bin/rousseau
./bin/rousseau version
```

### Via `go install`

```bash
go install github.com/sebastienrousseau/rousseau-agent/cmd/rousseau@latest
```

The binary is fully static (`CGO_ENABLED=0`) and embeds `modernc.org/sqlite`; there is no C toolchain or libc dependency at runtime.

### From a signed release

Every tagged release publishes a checksummed archive, a CycloneDX SBOM, a SLSA-3 provenance attestation, and a cosign signature of the checksum file.

```bash
cosign verify-blob \
  --certificate-identity-regexp 'sebastienrousseau/rousseau-agent' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --signature rousseau_<version>_checksums.txt.sig \
  rousseau_<version>_checksums.txt
```

## Quick start

### Terminal chat

```bash
rousseau chat
```

Bubble Tea TUI. Enter to send, `Ctrl+C` to quit. Session history persists to `~/.local/share/rousseau/sessions.db`.

### One of nine chat transports

```bash
# WhatsApp (QR-scan pairing on first launch)
rousseau whatsapp --allow 447000000000@s.whatsapp.net

# Slack Socket Mode
rousseau slack --app-token xapp-... --bot-token xoxb-...

# Discord Gateway
rousseau discord --token bot-token

# Email over IMAP + SMTP
rousseau email --imap-addr imap.example.com:993 --imap-username u --imap-password p \
               --smtp-addr smtp.example.com:587 --smtp-username u --smtp-password p \
               --from bot@example.com
```

`rousseau <transport> --help` lists per-transport flags. Every transport reads defaults from `~/.config/rousseau/config.yaml`.

### MCP server

```bash
rousseau mcp     # stdio, JSON-RPC 2.0, MCP spec 2024-11-05
```

## Deployment

The reference production deployment is a rootless Podman container managed by a systemd Quadlet unit — a single-node install that survives reboots, provides secure isolation without a Kubernetes dependency, and remains fully under operator control.

### Build the image

Three flavours are shipped; pick by size / feature reach:

```bash
# :latest — includes the claude CLI (node:22-alpine base, ~530 MB)
podman build -t rousseau-agent:local -f docker/Dockerfile .

# :distroless — no claude CLI, all transports (distroless static, ~54 MB)
podman build -t rousseau-agent:distroless -f docker/Dockerfile.distroless .

# :lite — no claude CLI, no WhatsApp (distroless static, ~47 MB)
podman build -t rousseau-agent:lite -f docker/Dockerfile.lite .
```

See [`docker/README.md`](./docker/README.md) for the full arch × variant size matrix and the "when to pick which" decision table.

### Install the Quadlet unit

```bash
mkdir -p ~/.config/containers/systemd
cp docker/rousseau-agent.container ~/.config/containers/systemd/
systemctl --user daemon-reload
systemctl --user start rousseau-agent.service
journalctl --user -u rousseau-agent.service -f
```

### Runtime posture (Quadlet unit)

| Setting | Value | Rationale |
|---|---|---|
| `Network=pasta` | Rootless network stack | `slirp4netns` was removed from recent Podman |
| `UserNS=keep-id` | Container UID 1000 → host UID 1000 | Bind-mounted files retain host ownership |
| `ReadOnly=true` | Root filesystem read-only | The binary can't mutate the image |
| `Tmpfs=/tmp:rw,size=64m,mode=1777` | Writable scratch | Anything the daemon writes lives on a bind mount |
| `DropCapability=all` + `NoNewPrivileges=true` | Least privilege | Outbound sockets need no elevated caps |
| `SeccompProfile=…` | Default seccomp filter | Kernel-level syscall gating |
| `Volume=%h/.local/share/rousseau:…rw,Z` | State persists | WhatsApp pairing + session store survive restarts |
| `Volume=%h/.claude:…rw,Z` | `claude` CLI auth | Reads / refreshes cached OAuth tokens |
| `Volume=%h/team-rousseau-workspace:/workspace:rw,Z` | Only the workspace is visible | Nothing else on the host is mounted |

### Kubernetes / OpenShift

`rousseau` is a stateless single-binary daemon; a minimal `Deployment` + `PersistentVolumeClaim` for the state directory is sufficient. Because there is no inbound HTTP surface, no `Service` or `Ingress` is required for outbound-WebSocket transports (Slack, Discord, WhatsApp, Matrix). Only inbound webhook-style transports would need a `Service` — and rousseau ships none by default.

## Configuration

`rousseau` resolves configuration in the order **flag > env > file > default**. The file lives at `~/.config/rousseau/config.yaml`:

```yaml
# LLM backend. Default "claudecli" shells out to the claude CLI and
# inherits its auth; "anthropic" | "bedrock" | "vertex" | "openai" |
# "openrouter" | "ollama" call an API directly.
provider: claudecli

anthropic:
  api_key: sk-ant-...
  model: claude-sonnet-4-6
  max_tokens: 4096

bedrock:
  region: us-east-1
  model: anthropic.claude-sonnet-4-6-20250101-v1:0
  profile: default

vertex:
  project: my-gcp-project
  region: us-central1
  model: claude-sonnet-4@20250101
  credentials_file: ~/.config/gcloud/vertex-key.json

claudecli:
  binary: claude
  model: sonnet
  permission_mode: bypassPermissions
  extra_args: []

log:
  level: info                    # debug, info, warn, error
  format: json                   # json for production

state:
  path: ~/.local/share/rousseau/sessions.db

agent:
  system_prompt: ""              # empty falls back to a sensible default
  max_iterations: 32
  skills_dir: ~/.config/rousseau/skills
  compression:
    enabled: true
    trigger_messages: 60
    keep_recent: 8
  approver:
    mode: pattern
    default: deny
    allow:
      - {tool: read, match: ".*"}
      - {tool: grep, match: ".*"}
      - {tool: edit, match: "^./workspace/.*"}
    deny:
      - {tool: bash, match: "rm -rf|sudo|:\\(\\)\\{ :\\|:& \\};:"}

slack: {app_token: "", bot_token: "", allowlist: []}
discord: {token: "", allowlist: []}
telegram: {token: "", allowlist: []}
matrix: {homeserver_url: "", access_token: "", user_id: "", allowlist: []}
signal: {account: "+44…", allowlist: []}
whatsapp: {reply_header: "💎 *Rousseau Agent*\n\n"}
imessage: {base_url: "http://localhost:1234", password: "", poll_interval: 5s}
sms: {provider: twilio, from: "+15550000000", account_sid: "AC…", auth_token: ""}
email:
  imap_addr: imap.example.com:993
  smtp_addr: smtp.example.com:587
  from: bot@example.com
  poll_interval: 30s
```

## Embedding the agent loop

`rousseau-agent` is a library as well as a daemon. The agent loop, tool registry, and provider abstractions have no CLI dependency; you can compose them into your own binary.

```go
package main

import (
    "context"
    "fmt"
    "log/slog"
    "os"

    "github.com/sebastienrousseau/rousseau-agent/internal/agent"
    "github.com/sebastienrousseau/rousseau-agent/internal/llm/claudecli"
    "github.com/sebastienrousseau/rousseau-agent/internal/tools"
    "github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
)

func main() {
    provider := claudecli.New(claudecli.Config{
        PermissionMode: "bypassPermissions",
    })

    registry := tools.NewRegistry()
    registry.MustRegister(builtin.NewReadTool())
    registry.MustRegister(builtin.NewGrepTool(0, 0))

    ag := agent.New(provider, registry,
        slog.New(slog.NewJSONHandler(os.Stdout, nil)),
        agent.Options{SystemPrompt: "You are a careful, concise coding assistant."})

    session := agent.NewSession("hello")
    session.Append(agent.NewUserText("What does main.go do?"))

    reply, err := ag.Turn(context.Background(), session)
    if err != nil {
        fmt.Fprintf(os.Stderr, "turn: %v\n", err)
        os.Exit(1)
    }
    fmt.Println(reply.Content[0].Text)
}
```

A complete example lives in [`examples/embed-agent`](./examples/embed-agent).

## Repository layout

```
cmd/rousseau/                    Entry point (signal handling + Execute)
internal/agent/                  Session, Message, Turn, agent loop, Provider interfaces, compression, image content
internal/agent/subagent/         Spawn N detached-copy tasks, aggregators, budget policy
internal/auth/oauth/             OAuth2 broker + XChaCha20-Poly1305 vault
internal/cli/                    Cobra command tree
internal/config/                 Viper-based; flag > env > file > default precedence
internal/cron/                   robfig/cron/v3 scheduler goroutine
internal/llm/anthropic/          Direct Anthropic API provider with cache markers
internal/llm/bedrock/            AWS Bedrock provider
internal/llm/claudecli/          Subprocess provider (claude CLI + JSON parser + image temp-files)
internal/llm/openai/             OpenAI-compatible provider (OpenAI, OpenRouter, Ollama, others)
internal/llm/vertex/             Google Vertex AI provider
internal/mcp/                    MCP server (JSON-RPC 2.0 over stdio, spec 2024-11-05)
internal/media/                  Image size/MIME policy consumed by every transport
internal/observability/          Prometheus registry (12 metrics) + OTel tracer
internal/observability/redact/   Redacting slog handler with default credential rules
internal/ratelimit/              Per-JID token bucket + KeyedLimiter
internal/recall/                 Hybrid vector + keyword recall (Voyage / OpenAI / Ollama / noop embedders)
internal/resilience/             Panic recover + circuit breaker (sony/gobreaker/v2)
internal/skills/                 agentskills.io-style skill loader
internal/state/                  Store interface + Summary type
internal/state/sqlite/           SQLite: WAL, JIDMap, claude cache, FTS5 recall, cron, oauth_tokens, recall_vectors
internal/tools/                  Tool interface + concurrency-safe Registry
internal/tools/builtin/          read, write, edit, grep, bash
internal/tools/integrations/     Native tool suites — github, slack, google, linear, stripe, composio
internal/transport/              Transport interface + Router (per-JID session, allowlist, dispatch)
internal/transport/whatsapp/     Whatsmeow-backed transport (compile-out behind //go:build no_whatsmeow)
internal/transport/{signal,telegram,matrix,slack,discord,sms,imessage,email}/
                                 Eight always-in transport adapters
internal/tui/                    Bubble Tea model (viewport, textarea, spinner, streaming)
docker/                          Dockerfile × 3 flavours, Podman Quadlet unit, egress-allowlist example, size table
docs/                            Competitor deep-dive, 3 WHY_NOT_* comparisons, implementation plan, release notes
examples/embed-agent/            Minimal library-embedding example
test/integration/soak/           Wall-clock soak harness with 5 leak invariants
```

The layered boundary is load-bearing. `agent` depends only on interfaces exposed by `tools`, on its own `Provider` types, and on the standard library. Concrete providers, stores, and transports depend on `agent` — never the reverse.

## Quality gates

Every commit runs, in CI:

- `go vet ./...`
- `golangci-lint run` (18 linters: bodyclose, copyloopvar, errcheck, errorlint, forbidigo, gocritic, govet, ineffassign, misspell, nilerr, nolintlint, revive, staticcheck, unconvert, unparam, unused, usestdlibvars, whitespace + gofmt & goimports formatters)
- `go test -race -count=1 -covermode=atomic ./...` on `ubuntu-latest` and `macos-latest`
- Coverage floor check (currently **88.7% avg per-package**; 15 packages at ≥ 90%)
- `govulncheck ./...`
- CodeQL default-setup scan (Go + actions, weekly + on-push)
- Reproducible build verification (bit-identical output on two consecutive builds)
- SLSA-3 provenance generation on tagged releases
- **Wall-clock soak** — 10-min synthetic workload with 5 leak invariants (push) / 30 min (PR) / 24 h (nightly)
- **Cross-arch matrix** — 12 combos verified per-push including RISC-V
- **Image-size budgets** — `:distroless` ≤ 70 MB, `:lite` ≤ 60 MB fail-on-regression
- **`-tags no_whatsmeow`** verified via a second `golangci-lint --build-tags=no_whatsmeow` pass

Local development mirrors CI via `make check`. Dependabot opens PRs for both `gomod` and `github-actions` groups.

## Security & disclosure

See [SECURITY.md](./SECURITY.md).

- **Vulnerability disclosure**: `sebastian.rousseau@gmail.com`. Acknowledgment within 72 hours.
- **Trust boundary**: the `bash` tool executes arbitrary commands with the user's privileges. Approval policies (pattern-mode with deny rules) are the operator's primary lever; sensible defaults ship with `bypassPermissions` refused unless the daemon is unattended.
- **Supply chain**: SLSA-3 provenance, cosign-signed checksums, CycloneDX SBOM, exact-pinned dependencies, `govulncheck` + CodeQL + Dependabot in CI.
- **Runtime**: read-only rootfs, all capabilities dropped, `NoNewPrivileges`, seccomp filter, non-root user, no inbound HTTP surface.

## Comparison

See [`docs/COMPETITORS_2026_07_12.md`](./docs/COMPETITORS_2026_07_12.md) for the full landscape audit and three head-to-head comparisons — [`WHY_NOT_TRUSTCLAW.md`](./docs/WHY_NOT_TRUSTCLAW.md), [`WHY_NOT_OPENCLAW.md`](./docs/WHY_NOT_OPENCLAW.md), [`WHY_NOT_ZEROCLAW.md`](./docs/WHY_NOT_ZEROCLAW.md).

Short version of where rousseau wins on a self-hosted-enterprise checklist:

| Requirement | rousseau | Cloud-hosted alternatives |
|---|:---:|:---:|
| Runs entirely inside operator infrastructure | ✅ | ❌ |
| No SaaS control plane, license server, or telemetry | ✅ | ❌ |
| SLSA-3 provenance + cosign + SBOM | ✅ | varies |
| Multiple LLM providers behind one binary | ✅ | rarely |
| Nine chat transports, no broker required | ✅ | 0–3 typical |
| 26 native tool integrations + Composio (1000+) | ✅ | 0–5 typical |
| MCP server (tools + sessions exposed) | ✅ | some |
| Sub-agent parallelism + vector recall | ✅ | some |
| Cross-arch (armv6/armv7/arm64/riscv64) | ✅ | ❌ (typically amd64/arm64 only) |
| Rootless container with dropped capabilities | ✅ | rarely documented |

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md).

- Commit style: [Conventional Commits](https://www.conventionalcommits.org/).
- Every exported identifier has a godoc comment.
- No `interface{}` / `any` in exported APIs without a written justification.

## License

Released under the [MIT License](./LICENSE) © 2026 Sebastien Rousseau.
