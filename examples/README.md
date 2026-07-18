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

Examples import from `github.com/sebastienrousseau/rousseau-agent/pkg/...` — the public library façade over the `internal/` implementation. External consumers can import `pkg/` verbatim; the `internal/` packages remain the source of truth but are not part of the stable API surface.
