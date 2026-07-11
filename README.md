# rousseau-agent

A private, enterprise-grade coding assistant for the terminal. Written in Go, powered by Anthropic Claude, with a Bubble Tea TUI and a Cobra CLI.

## Status

`v0` scaffold. Runnable chat with tool use over Anthropic. Session persistence, streaming, and additional tools land next.

## Requirements

- Go 1.26+
- `ANTHROPIC_API_KEY` in the environment (or in `~/.config/rousseau/config.yaml`)

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
