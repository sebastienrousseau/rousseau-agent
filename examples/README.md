# Examples

Small, self-contained programs that exercise slices of the `rousseau-agent` API.

| Directory | What it shows |
|---|---|
| [`embed-agent/`](./embed-agent/) | Embed the agent loop in your own program, pick a provider, register tools, and drive a single conversation Turn. |
| [`embed-subagent/`](./embed-subagent/) | Fan a single parent session into N sub-agent Tasks with bounded concurrency + per-task timeout + aggregate token budget; combine the results via the default aggregator. |
| [`embed-recall/`](./embed-recall/) | Ingest messages into the sqlite-backed vector store, then run a hybrid (vector + keyword) recall query. Uses the noop embedder so the example runs without an API key. |
| [`embed-integrations/`](./embed-integrations/) | Register every enabled native tool suite (GitHub / Slack / Linear / Stripe / Google / Composio) into a `tools.Registry` using environment-driven credentials. |

Run any example with:

```bash
go run ./examples/<name>
```

Examples import from `github.com/sebastienrousseau/rousseau-agent/internal/...`. Rousseau's `internal/` packages are intentionally not part of a stable public API — the examples show the shape you would replicate if you wanted to build on top of the library. If you need long-term stability, vendor the pieces you care about.
