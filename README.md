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

## Layout

```
cmd/rousseau/          Entry point (thin: wires cobra root and runs)
internal/agent/        Domain: Session, Message, Turn, agent loop
internal/llm/          Provider interface + Anthropic implementation
internal/tools/        Tool interface, registry, and built-in tools
internal/state/        Store interface + SQLite implementation
internal/config/       Layered config: flag > env > file > defaults
internal/tui/          Bubble Tea models and views
internal/cli/          Cobra command tree
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
