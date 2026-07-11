# rousseau-agent

A private, enterprise-grade coding assistant for the terminal. Written in Go, powered by Anthropic Claude, with a Bubble Tea TUI and a Cobra CLI.

## Status

`v0` scaffold. Runnable chat with tool use over Anthropic. Session persistence, streaming, and additional tools land next.

## Requirements

- Go 1.26+
- One of:
  - **`claude` CLI on `$PATH`** (default) — inherits whatever Claude Code auth you've already set up. Nothing to configure.
  - **`ANTHROPIC_API_KEY`** — required only if you set `provider: anthropic` in the config.

## Providers

`rousseau` ships two LLM backends. The default is `claudecli`, which shells out to the local `claude` CLI. This uses your existing Claude Code subscription / auth — you never handle an API key.

To switch to the direct Anthropic API, drop this in `~/.config/rousseau/config.yaml`:

```yaml
provider: anthropic
anthropic:
  api_key: sk-ant-...    # or set ANTHROPIC_API_KEY
  model: claude-sonnet-4-6
```

Because `claudecli` runs its own tool-use loop internally, the built-in tools (`read`, `write`, `edit`, `grep`, `bash`) are only exercised when `provider: anthropic`. Under `claudecli`, tools are handled inside the claude subprocess.

## Build

```bash
make build
./bin/rousseau chat
```

Or install directly:

```bash
go install github.com/sebastienrousseau/rousseau-agent/cmd/rousseau@latest
```

## Commands

```
rousseau chat        Open the interactive Bubble Tea chat.
rousseau whatsapp    Run the WhatsApp bridge (see caveats below).
rousseau version     Print version, commit, and build date.
```

## WhatsApp bridge

`rousseau whatsapp` runs a foreground daemon that connects to WhatsApp Web via the reverse-engineered `whatsmeow` client. On first launch it prints a QR code — scan it from your phone under **Settings → Linked devices**. Device credentials are cached at `~/.local/share/rousseau/whatsapp.db`; subsequent launches connect silently.

```
rousseau whatsapp --allow 15551234567@s.whatsapp.net
```

For unattended use (the daemon has no way to prompt you for tool approval), configure the CLI provider to auto-approve inside the config file:

```yaml
claudecli:
  permission_mode: bypassPermissions   # or acceptEdits for less blast radius
```

**Caveats.** This uses the UNOFFICIAL WhatsApp Web protocol. Meta occasionally bans numbers that use unofficial clients. Do not run this on a number you rely on. Always pass `--allow` in production — omitting it lets anyone who messages your number talk to the agent.

## Layout

```
cmd/rousseau/                 Entry point (thin: wires cobra root and runs)
internal/agent/               Domain: Session, Message, Turn, agent loop
internal/llm/                 Provider interface + Anthropic implementation
internal/tools/               Tool interface, registry, and built-in tools
internal/tools/builtin/       Reference tools: read, write, edit, grep, bash
internal/state/               Store interface + SQLite implementation
internal/config/              Layered config: flag > env > file > defaults
internal/tui/                 Bubble Tea models and views
internal/cli/                 Cobra command tree
internal/transport/           Transport abstraction + Router
internal/transport/whatsapp/  WhatsApp implementation via whatsmeow
```

The layered boundary matters. `agent` depends on `llm`, `tools`, and `state` interfaces — never on their concrete implementations. This keeps the core testable and the framework re-targetable.

## Quality gates

Every commit runs through:

- `go vet ./...`
- `golangci-lint run` (strict config)
- `go test ./...` (unit + integration)
- `govulncheck ./...`
- Race detector on CI

CI fails on any drop in coverage below the configured floor.

## License

MIT. See [LICENSE](./LICENSE).
