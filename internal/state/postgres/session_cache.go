package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// claudeSessionsSchema is the Postgres-flavoured mirror of the
// SQLite schema. Differences from the SQLite version and why:
//
//   - `seen_at TIMESTAMPTZ DEFAULT NOW()` instead of TEXT — the
//     SQLite driver formats time manually because SQLite has no
//     native timestamp type; Postgres does, so let the DB stamp
//     it. Removes a round of string formatting from the hot path.
//   - TEXT PRIMARY KEY on session_id preserves the exact ID
//     Claude Code emits — UUIDs today, but the type is opaque so
//     we do not narrow to UUID.
const claudeSessionsSchema = `
CREATE TABLE IF NOT EXISTS claude_sessions (
    session_id TEXT PRIMARY KEY,
    seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`

// ClaudeSessionCache is a Postgres-backed implementation of
// claudecli.SessionCache. Recently-seen IDs are hot-cached in
// memory to keep the fast path zero-latency; misses fall through
// to the DB.
//
// Interface-compatible with [sqlite.ClaudeSessionCache] — same
// method signatures so the daemon can hold either concrete type.
type ClaudeSessionCache struct {
	db  *sql.DB
	mu  sync.Mutex
	hot map[string]struct{}
}

// NewClaudeSessionCache attaches to an existing Postgres [Store]
// and applies the schema. Idempotent — safe to call on every
// daemon boot.
func NewClaudeSessionCache(ctx context.Context, s *Store) (*ClaudeSessionCache, error) {
	if _, err := s.db.ExecContext(ctx, claudeSessionsSchema); err != nil {
		return nil, fmt.Errorf("postgres: apply claude_sessions schema: %w", err)
	}
	return &ClaudeSessionCache{db: s.db, hot: map[string]struct{}{}}, nil
}

// IsKnown reports whether id has been seen previously. Mirrors
// the SQLite driver: hot-cache first, DB on miss, promote to
// hot-cache on DB hit so subsequent lookups skip the query.
func (c *ClaudeSessionCache) IsKnown(id string) bool {
	c.mu.Lock()
	if _, ok := c.hot[id]; ok {
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()

	const q = `SELECT 1 FROM claude_sessions WHERE session_id = $1`
	var one int
	err := c.db.QueryRowContext(context.Background(), q, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		return false
	}
	c.mu.Lock()
	c.hot[id] = struct{}{}
	c.mu.Unlock()
	return true
}

// Remember persists id and updates the hot cache. ON CONFLICT DO
// NOTHING keeps Remember idempotent — Claude Code may re-emit a
// session ID we already know and we must not error.
func (c *ClaudeSessionCache) Remember(id string) {
	c.mu.Lock()
	c.hot[id] = struct{}{}
	c.mu.Unlock()

	const q = `
INSERT INTO claude_sessions (session_id)
VALUES ($1)
ON CONFLICT (session_id) DO NOTHING
`
	if _, err := c.db.ExecContext(context.Background(), q, id); err != nil {
		slog.Warn("postgres: remember claude session", "id", id, "err", err)
	}
}
