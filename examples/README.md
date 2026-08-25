# Examples

Small, self-contained programs that exercise slices of the `rousseau-agent` API.

| Directory | What it shows |
|---|---|
| [`embed-agent/`](./embed-agent/) | Embed the agent loop in your own program, pick a provider, register tools, and drive a single conversation Turn. |
| [`embed-subagent/`](./embed-subagent/) | Fan a single parent session into N sub-agent Tasks with bounded concurrency + per-task timeout + aggregate token budget; combine the results via the default aggregator. |
| [`embed-recall/`](./embed-recall/) | Ingest messages into the sqlite-backed vector store, then run a hybrid (vector + keyword) recall query. Uses the noop embedder so the example runs without an API key. |
| [`embed-integrations/`](./embed-integrations/) | Register every enabled native tool suite (GitHub / Slack / Linear / Stripe / Google / Composio) into a `tools.Registry` using environment-driven credentials. |
| [`embed-router/`](./embed-router/) | Compose a multi-model routing provider with three rules (short chat → haiku, tool-heavy → opus, default → sonnet); drive three test requests through it and print which child fired. |
| [`embed-hooks/`](./embed-hooks/) | Wire two shell-script lifecycle hooks into `pre_tool_use`; the audit hook allows everything, the safety hook denies `rm -rf`. Shows first-deny-wins semantics. |
| [`embed-cost/`](./embed-cost/) | Record simulated completion usage into the `session_costs` SQLite table; query per-session sums and top-cost sessions. Uses `pricing.DefaultTable` — no API key required. |
| [`embed-identity/`](./embed-identity/) | Provision a WhatsApp identity, link a Slack handle to it, then verify both handles resolve to the same identity ID (cross-transport session continuity). |

Run any example with:

```bash
go run ./examples/<name>
```

Examples import from `github.com/sebastienrousseau/rousseau-agent/pkg/...` — the public library façade over the `internal/` implementation. External consumers can import `pkg/` verbatim; the `internal/` packages remain the source of truth but are not part of the stable API surface.
