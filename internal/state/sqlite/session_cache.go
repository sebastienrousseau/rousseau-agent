package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

const claudeSessionsSchema = `
CREATE TABLE IF NOT EXISTS claude_sessions (
    session_id  TEXT PRIMARY KEY,
    seen_at     TEXT NOT NULL
);
`

// ClaudeSessionCache is a SQLite-backed implementation of
// claudecli.SessionCache. Recently-seen IDs are hot-cached in memory to
// keep the fast path zero-latency; misses fall through to the DB.
type ClaudeSessionCache struct {
	db  *sql.DB
	mu  sync.Mutex
	hot map[string]struct{}
}

// NewClaudeSessionCache returns a cache that shares s's SQLite
// database. Idempotent — safe to call multiple times.
func NewClaudeSessionCache(ctx context.Context, s *Store) (*ClaudeSessionCache, error) {
	if _, err := s.db.ExecContext(ctx, claudeSessionsSchema); err != nil {
		return nil, fmt.Errorf("sqlite: apply claude_sessions schema: %w", err)
	}
	return &ClaudeSessionCache{db: s.db, hot: map[string]struct{}{}}, nil
}

// IsKnown reports whether id has been seen previously.
func (c *ClaudeSessionCache) IsKnown(id string) bool {
	c.mu.Lock()
	if _, ok := c.hot[id]; ok {
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()

	const q = `SELECT 1 FROM claude_sessions WHERE session_id = ?`
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

// Remember persists id and updates the hot cache.
func (c *ClaudeSessionCache) Remember(id string) {
	c.mu.Lock()
	c.hot[id] = struct{}{}
	c.mu.Unlock()

	const q = `
INSERT INTO claude_sessions (session_id, seen_at)
VALUES (?, ?)
ON CONFLICT(session_id) DO NOTHING
`
	_, _ = c.db.ExecContext(context.Background(), q, id,
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
}
