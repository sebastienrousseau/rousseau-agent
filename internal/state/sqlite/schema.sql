CREATE TABLE IF NOT EXISTS sessions (
    id             TEXT PRIMARY KEY,
    title          TEXT NOT NULL,
    payload        TEXT NOT NULL,
    message_count  INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    sender         TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sessions_updated_at
    ON sessions(updated_at DESC);

-- The (sender, updated_at DESC) index is created by
-- ensureSenderColumn in store.go rather than here. A legacy DB
-- without the sender column would fail this CREATE INDEX at
-- boot (schema.sql runs BEFORE the migration helper), so we
-- defer the index to the migration path. Fresh installs get
-- the same index via that helper — CREATE INDEX IF NOT EXISTS
-- is idempotent on both paths.
