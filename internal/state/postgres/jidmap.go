package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// jidMapSchema is the Postgres-flavoured mirror of the SQLite
// schema. Differences from the SQLite version and why:
//
//   - `created_at TIMESTAMPTZ` instead of TEXT — same reasoning
//     as sessions + cron_jobs: native time types give proper
//     range queries and index usage.
//   - `DEFAULT NOW()` so the driver does not have to synthesise a
//     wall-clock string on every Put — the SQLite driver formats
//     time manually because SQLite has no TIMESTAMPTZ.
//   - TEXT PRIMARY KEY on jid preserves the exact string operators
//     see (e.g. "1234@s.whatsapp.net") — no casing / normalisation.
const jidMapSchema = `
CREATE TABLE IF NOT EXISTS jid_sessions (
    jid        TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`

// JIDMap is a Postgres-backed transport-JID → session-id map.
// Interface-compatible with [sqlite.JIDMap] — matching signatures
// let the daemon hold either concrete type without a shared
// interface (which would just re-declare what the two mirror).
type JIDMap struct {
	db *sql.DB
}

// NewJIDMap attaches to an existing Postgres [Store] and applies
// the schema. Idempotent — safe to call on every daemon boot.
func NewJIDMap(ctx context.Context, s *Store) (*JIDMap, error) {
	if _, err := s.db.ExecContext(ctx, jidMapSchema); err != nil {
		return nil, fmt.Errorf("postgres: apply jid schema: %w", err)
	}
	return &JIDMap{db: s.db}, nil
}

// Get returns the session id mapped to jid, or ok=false if absent.
func (m *JIDMap) Get(ctx context.Context, jid string) (string, bool, error) {
	const q = `SELECT session_id FROM jid_sessions WHERE jid = $1`
	var id string
	err := m.db.QueryRowContext(ctx, q, jid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("postgres: get jid: %w", err)
	}
	return id, true, nil
}

// Put records jid → sessionID, replacing any previous mapping.
// ON CONFLICT DO UPDATE mirrors the SQLite driver's upsert so
// callers see identical semantics.
func (m *JIDMap) Put(ctx context.Context, jid, sessionID string) error {
	const q = `
INSERT INTO jid_sessions (jid, session_id)
VALUES ($1, $2)
ON CONFLICT (jid) DO UPDATE SET session_id = EXCLUDED.session_id
`
	if _, err := m.db.ExecContext(ctx, q, jid, sessionID); err != nil {
		return fmt.Errorf("postgres: put jid: %w", err)
	}
	return nil
}
