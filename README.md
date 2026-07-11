<!-- markdownlint-disable MD033 MD041 -->

<p align="center">
  <a href="https://github.com/sebastienrousseau/rousseau-agent">
    <img src="docs/assets/logo.svg" alt="rousseau-agent" width="180" onerror="this.style.display='none'"/>
  </a>
</p>

<h1 align="center">rousseau-agent</h1>

<p align="center">
  A private, enterprise-grade coding assistant that lives in your terminal — and in WhatsApp when you're not at your desk.
</p>

<p align="center">
  <a href="https://github.com/sebastienrousseau/rousseau-agent/actions/workflows/ci.yml">
    <img src="https://img.shields.io/github/actions/workflow/status/sebastienrousseau/rousseau-agent/ci.yml?branch=main&label=CI&style=for-the-badge" alt="CI status"/>
  </a>
  <a href="https://pkg.go.dev/github.com/sebastienrousseau/rousseau-agent">
    <img src="https://img.shields.io/badge/pkg.go.dev-reference-informational?style=for-the-badge&logo=go" alt="Go reference"/>
  </a>
  <a href="https://goreportcard.com/report/github.com/sebastienrousseau/rousseau-agent">
    <img src="https://img.shields.io/badge/report-A+-brightgreen?style=for-the-badge&logo=go" alt="Go Report Card"/>
  </a>
  <a href="https://github.com/sebastienrousseau/rousseau-agent/blob/main/LICENSE">
    <img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="License: MIT"/>
  </a>
  <img src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go 1.26+"/>
</p>

---

## Table of contents

- [Why rousseau-agent](#why-rousseau-agent)
- [Features](#features)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Usage](#usage)
- [Configuration](#configuration)
- [Container deployment](#container-deployment)
- [Package layout](#package-layout)
- [Quality gates](#quality-gates)
- [Security](#security)
- [Contributing](#contributing)
- [License](#license)

---

## Why rousseau-agent

`rousseau-agent` is what a coding assistant looks like when it is written by one engineer, for one engineer, with no ambition to become a product:

- **Small.** ~4,000 lines of Go. Every module fits in one head.
- **Yours.** No API keys to plumb, no accounts to create — the default provider shells out to your locally-installed `claude` CLI, inheriting whatever authentication Claude Code already has.
- **Reachable.** A [`whatsmeow`](https://go.mau.fi/whatsmeow)-backed WhatsApp bridge lets you talk to it from your phone. Same brain, different terminal.
- **Contained.** Ships as a rootless Podman container with least-privilege bind mounts. Nothing on the host is visible except your workspace, your rousseau state directory, and your Claude Code auth.

## Features

- **Two LLM providers** — `claudecli` (subprocess, no keys) and `anthropic` (direct API, exact-pinned SDK).
- **Bubble Tea TUI** with viewport, scrollback, and typing indicator.
- **Five built-in tools** — `read`, `write`, `edit`, `grep`, `bash`; strict JSON-schema inputs; unique-string constraint on `edit` prevents accidental mass-replacement.
- **Persistent SQLite session store** — WAL journaling, `busy_timeout=15s`, indexed on `updated_at`.
- **WhatsApp bridge** — QR-scan pairing, per-JID session isolation, allowlist, branded reply header, live typing indicator, LID-to-phone-JID normalisation.
- **Persistent claude-session cache** — first turn after a daemon restart uses `--resume`, not a wasted `--session-id` round-trip.
- **Podman Quadlet unit** for systemd-managed autostart with `keep-id` user-namespace mapping.
- **Strict CI** — `go vet`, `golangci-lint v2`, race-enabled tests on Linux + macOS, `govulncheck`, CodeQL, coverage floor, Dependabot for both `gomod` and `github-actions`.

## Installation

### Prerequisites

- Go **1.26+**
- One of:
  - The `claude` CLI on `$PATH` (default — inherits your Claude Code auth); or
  - `ANTHROPIC_API_KEY` set in the environment (only required when `provider: anthropic`).

### From source

```bash
git clone https://github.com/sebastienrousseau/rousseau-agent
cd rousseau-agent
make build            # produces ./bin/rousseau
./bin/rousseau version
```

### Via `go install`

```bash
go install github.com/sebastienrousseau/rousseau-agent/cmd/rousseau@latest
```

The binary is fully static (`CGO_ENABLED=0`); it embeds `modernc.org/sqlite` so no C toolchain or libc coupling is required at runtime.

## Quick start

### Terminal chat

```bash
rousseau chat
```

Opens the interactive TUI. Type, press **Enter** to send; **Ctrl+C** to quit. Session history is persisted to `~/.local/share/rousseau/sessions.db`.

### WhatsApp bridge

```bash
rousseau whatsapp --allow 15551234567@s.whatsapp.net
```

On first launch, a QR code renders in the terminal. Scan it from your phone under **Settings → Linked devices → Link a device**. The daemon stays foreground; message yourself and get a reply headed with **💎 *Rousseau Agent***.

## Usage

Programmatic use — build your own transport or embed the agent loop:

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
	// The claudecli provider shells out to the local `claude` CLI. No
	// API key handling; auth is whatever Claude Code already has.
	provider := claudecli.New(claudecli.Config{
		PermissionMode: "bypassPermissions",
	})

	// Register any tools you want the model to be able to call. The
	// claudecli provider handles tools inside the subprocess, so the
	// registry is only exercised for the direct anthropic provider.
	registry := tools.NewRegistry()
	registry.MustRegister(builtin.NewReadTool())
	registry.MustRegister(builtin.NewGrepTool(0, 0))

	// Compose the agent.
	ag := agent.New(provider, registry, slog.New(slog.NewTextHandler(os.Stdout, nil)), agent.Options{
		SystemPrompt: "You are a careful, concise coding assistant.",
	})

	// A Session carries the conversation history; NewSession issues a
	// UUID that is fed through to `claude --session-id` for continuity.
	session := agent.NewSession("first")
	session.Append(agent.NewUserText("Hello, who are you?"))

	// Turn advances the conversation by one round-trip and returns the
	// final assistant message. Tool calls (if any) are run and their
	// results appended to the session automatically.
	reply, err := ag.Turn(context.Background(), session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "turn: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(reply.Content[0].Text)
}
```

## Configuration

`rousseau` resolves configuration in the order **flag > env > file > default**, with the file at `~/.config/rousseau/config.yaml`:

```yaml
# LLM backend. "claudecli" (default) shells out to the claude CLI and
# inherits its auth; "anthropic" hits the API directly and needs a key.
provider: claudecli

anthropic:
  api_key: sk-ant-...            # ignored unless provider=anthropic
  model: claude-sonnet-4-6
  max_tokens: 4096

claudecli:
  binary: claude                 # override if not on $PATH
  model: sonnet                  # alias or full name; empty uses claude's default
  permission_mode: bypassPermissions   # unattended daemons need this
  extra_args: []                 # appended to every invocation

log:
  level: info                    # debug, info, warn, error
  format: text                   # text or json

state:
  path: ~/.local/share/rousseau/sessions.db

agent:
  system_prompt: ""              # empty falls back to a sensible default
  max_iterations: 32             # cap on tool-use rounds per turn

whatsapp:
  reply_header: "💎 *Rousseau Agent*\n\n"   # single space " " disables
```

## Container deployment

The recommended production posture is a rootless Podman container managed by a systemd Quadlet unit.

### Build the image

```bash
podman build -t rousseau-agent:local -f docker/Dockerfile .
```

The image is a multi-stage build: a `golang:1.26-alpine` builder produces a static binary, then a `node:22-alpine` runtime installs the `claude` CLI and copies the binary in. Final size ~530 MB (vs multi-GB for stacks that ship their own Python runtime).

### Install the Quadlet unit

```bash
mkdir -p ~/.config/containers/systemd
cp docker/rousseau-agent.container ~/.config/containers/systemd/
systemctl --user daemon-reload
systemctl --user start rousseau-agent.service
journalctl --user -u rousseau-agent.service -f
```

The unit configures:

| Setting | Value | Why |
|---|---|---|
| `Network=pasta` | Rootless network stack. | slirp4netns was removed from recent podman. |
| `UserNS=keep-id` | Container UID 1000 → host UID 1000. | Bind-mounted files stay owned by you on both sides. |
| `ReadOnly=true` | Root filesystem is read-only. | The binary can't mutate the image. |
| `Tmpfs=/tmp:rw,size=64m,mode=1777` | Writable scratch. | Anything the daemon actually needs to write goes on a bind mount. |
| `DropCapability=all` + `NoNewPrivileges=true` | Least privilege. | The Go binary opens outbound sockets; no elevated caps required. |
| `SeccompProfile=…seccomp.json` | Default seccomp filter. | Kernel-level syscall gating. |
| `Volume=%h/.local/share/rousseau:…:rw,Z` | State persists across restarts. | WhatsApp pairing and session store survive. |
| `Volume=%h/.claude:…:rw,Z` | `claude` CLI auth. | Reads / refreshes cached OAuth tokens. |
| `Volume=%h/team-rousseau-workspace:/workspace:rw,Z` | Only this workspace is visible. | Nothing else on the host is mounted. |

## Package layout

```
cmd/rousseau/                 Entry point (12 lines: signal handling + Execute)
internal/agent/               Domain: Session, Message, Turn, agent loop, Provider interface
internal/llm/anthropic/       Direct Anthropic API provider (anthropic-sdk-go)
internal/llm/claudecli/       Subprocess provider (claude CLI + JSON output parser)
internal/tools/               Tool interface + concurrency-safe Registry + Definition
internal/tools/builtin/       read, write, edit, grep, bash
internal/state/               Store interface + Summary type
internal/state/sqlite/        SQLite implementation (WAL, busy_timeout, JIDMap, claude cache)
internal/config/              Viper-based; flag > env > file > default precedence
internal/tui/                 Bubble Tea model (viewport, textarea, spinner)
internal/cli/                 Cobra command tree (chat, whatsapp, version, provider selection)
internal/transport/           Transport interface + Router (per-JID session, allowlist)
internal/transport/whatsapp/  whatsmeow-backed WhatsApp bridge
docker/                       Dockerfile + Podman Quadlet unit
```

The layered boundary is load-bearing. `agent` depends only on interfaces exposed by `tools`, on its own `Provider` type, and on the standard library. Concrete providers, stores, and transports depend on `agent` — never the reverse. This is the pattern that keeps `cli.py`-scale monoliths from re-emerging.

## Quality gates

Every commit runs, in CI:

- `go vet ./...`
- `golangci-lint run` (strict — `no fmt.Print*` in library code, no panics outside `main`)
- `go test -race -count=1 ./...` on `ubuntu-latest` and `macos-latest`
- `govulncheck ./...`
- CodeQL static analysis (Go)
- Coverage floor check

Local development mirrors CI via `make check`. Dependabot opens weekly PRs for both `gomod` and `github-actions` groups.

## Security

- **Direct dependencies pinned** to exact versions in `go.mod`; transitive resolution frozen in `go.sum`.
- **`govulncheck` in CI** — the build breaks on a known vulnerability in any imported package.
- **CodeQL on every PR** — semantic static analysis for Go.
- **Container hardening** — read-only rootfs, all capabilities dropped, `NoNewPrivileges`, seccomp filter, non-root user, no published ports.
- **No secrets in the image.** Authentication is bind-mounted from the host at runtime.
- **Vulnerability disclosure** — see [SECURITY.md](./SECURITY.md). Private channel: `sebastian.rousseau@gmail.com`.

## Roadmap

- [`docs/ROADMAP.md`](./docs/ROADMAP.md) — living implementation plan (P0/P1/P2 priorities, exit criteria, non-negotiable engineering standards).
- [`docs/COMPETITORS.md`](./docs/COMPETITORS.md) — 2026 landscape, competitor deep-dive, honest rating against Hermes/Claude Code/Aider/Cursor/Devin/OpenHands.

## Contributing

Private project; contributions are accepted from invited collaborators only. Please read [CONTRIBUTING.md](./CONTRIBUTING.md) before opening a PR.

- Commit style: [Conventional Commits](https://www.conventionalcommits.org/).
- Every exported identifier has a godoc comment.
- No `interface{}` / `any` in exported APIs without a written justification.

## License

Released under the [MIT License](./LICENSE) © 2026 Sebastien Rousseau.
