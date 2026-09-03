-- The sessions table mirrors the SQLite schema. Kept
-- deliberately minimal: only the columns the state.Store
-- interface reads/writes. Extension tables (cron, jidmap,
-- oauth, recall, session_cache, session_costs) stay
-- SQLite-local for now — they were designed for a single-
-- replica daemon and will port in a follow-up.
--
-- Notes on the schema choices below:
--
--   * TEXT PRIMARY KEY for id — session IDs are already UUIDv7
--     strings; storing as TEXT avoids the "which UUID type"
--     bikeshed and keeps the row-level shape identical across
--     drivers.
--   * TIMESTAMPTZ for created_at / updated_at — Postgres native
--     time type. SQLite uses ISO-8601 TEXT; the driver code
--     handles the conversion. Storing native TIMESTAMPTZ gives
--     us proper range queries + index usage.
--   * payload TEXT (not JSONB) — the Session is JSON-marshalled
--     by the caller. Storing as TEXT keeps the write path
--     byte-identical to SQLite and defers "should we query into
--     the payload?" until we have a concrete use case.
--   * ON CONFLICT DO UPDATE — matches SQLite's upsert pattern
--     one-for-one so the driver code stays a thin wrapper.
CREATE TABLE IF NOT EXISTS sessions (
    id             TEXT PRIMARY KEY,
    title          TEXT NOT NULL,
    payload        TEXT NOT NULL,
    message_count  INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL,
    sender         TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sessions_updated_at
    ON sessions(updated_at DESC);

-- Per-sender secondary index: the /sessions chat verb filters
-- by sender, so the query has to hit an index rather than seq-
-- scan a large sessions table on every list.
CREATE INDEX IF NOT EXISTS idx_sessions_sender_updated
    ON sessions(sender, updated_at DESC);
