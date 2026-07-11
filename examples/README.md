# Examples

Small, self-contained programs that exercise slices of the `rousseau-agent` API.

| Directory | What it shows |
|---|---|
| [`embed-agent/`](./embed-agent/) | Embed the agent loop in your own program, pick a provider, register tools, and drive a single conversation Turn. |

Run any example with:

```bash
go run ./examples/<name>
```

Examples import from `github.com/sebastienrousseau/rousseau-agent/internal/...`. Rousseau's `internal/` packages are intentionally not part of a stable public API — the examples show the shape you would replicate if you wanted to build on top of the library. If you need long-term stability, vendor the pieces you care about.
