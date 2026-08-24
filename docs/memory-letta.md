# Letta-style self-editing memory (W4.1)

**Status:** [`internal/memory/letta`](../internal/memory/letta)
package skeleton shipped in `v0.0.1`; runtime is a follow-up.

## Why

The current [`internal/recall`](../internal/recall) provider treats
memory as chunks + vector similarity — great for information
retrieval, weak for the agent to reason about *its own state*.

The frontier (Letta / MemGPT, Google's Always-On Memory Agent, July
2026) splits memory into two tiers:

- **Core memory** — small, always in context, editable by the agent
  via tool calls (`memory_write_core`, `memory_read_core`,
  `memory_delete_core`). Where identity-scoped facts live: "user
  prefers concise replies", "working on rust project X."
- **Archival memory** — large vector-searchable store the agent
  queries on demand (`memory_search_archival`,
  `memory_add_to_archival`). Chunk-level, roughly what today's
  recall already does.

The agent decides what to promote (core → archival, when core fills
up) and what to search (archival → context, when the user asks about
prior work). This is state the agent *manages*, not a knowledge base
it *searches*.

## Design

### Store contract

Already defined in [`internal/memory/letta`](../internal/memory/letta):

```go
type Store interface {
    LoadCore(ctx, sessionID string) (CoreMemory, error)
    WriteCore(ctx, m CoreMemory) error
    SearchArchival(ctx, sessionID, query string, limit int) ([]ArchivalEntry, error)
    AppendArchival(ctx, e ArchivalEntry) error
    DemoteOldest(ctx, sessionID string, n int) error
}
```

### SQLite schema (planned)

```sql
CREATE TABLE letta_core (
    session_id   TEXT PRIMARY KEY,
    facts_json   TEXT NOT NULL,        -- JSON array of {key, value, created_at, updated_at}
    max_bytes    INTEGER NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE letta_archival (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id   TEXT NOT NULL,
    text         TEXT NOT NULL,
    embedding    BLOB NOT NULL,         -- packed float32 array
    created_at   TEXT NOT NULL
);

CREATE INDEX idx_letta_archival_session ON letta_archival(session_id);
```

Reuses the existing embedding provider from `internal/recall`.

### Four tools registered with the agent

- `memory_write_core(key, value)` — upsert into core. Triggers
  `DemoteOldest` if MaxBytes exceeded.
- `memory_read_core()` — return the whole core memory (small — no
  need for search).
- `memory_search_archival(query, limit)` — semantic search over
  archival.
- `memory_add_to_archival(text)` — explicit promotion; also happens
  automatically via DemoteOldest.

### System-prompt integration

The system prompt gains a `<core_memory>` block that renders every
Fact currently in core. Refreshed once per Turn (via a new
`agent.MemoryProvider` interface — parallel to the existing
`SkillsProvider` and `RecallProvider`).

## Migration path

- `memory.model: chunks` (default) — current recall provider.
- `memory.model: letta` — opt-in to the new store. Sessions with
  existing recall chunks are lazily migrated: on first
  SearchArchival call, chunks are ingested into `letta_archival`
  with fresh embeddings.

## Success metric

After 20+ turns on a session, the core memory holds the user's
preferences and current work context *without operator
intervention*. Measured by an eval script: run 20 canned turns, then
ask "what am I working on?" and check the answer references facts
the agent inferred, not fresh restatements.
