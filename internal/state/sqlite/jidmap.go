package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// jidMapSchema is applied when a JIDMap is opened. Idempotent.
const jidMapSchema = `
CREATE TABLE IF NOT EXISTS jid_sessions (
    jid         TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL,
    created_at  TEXT NOT NULL
);
`

// JIDMap persists platform-sender-to-Session-id mappings for
// long-running transports (WhatsApp, Telegram, …).
type JIDMap struct {
	db *sql.DB
}

// NewJIDMap returns a JIDMap that shares the passed Store's SQLite
// database.
func NewJIDMap(ctx context.Context, s *Store) (*JIDMap, error) {
	if _, err := s.db.ExecContext(ctx, jidMapSchema); err != nil {
		return nil, fmt.Errorf("sqlite: apply jid schema: %w", err)
	}
	return &JIDMap{db: s.db}, nil
}

// Get returns the session id mapped to jid, or ok=false if absent.
func (m *JIDMap) Get(ctx context.Context, jid string) (string, bool, error) {
	const q = `SELECT session_id FROM jid_sessions WHERE jid = ?`
	var id string
	err := m.db.QueryRowContext(ctx, q, jid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("sqlite: get jid: %w", err)
	}
	return id, true, nil
}

// Put records jid → sessionID, replacing any previous mapping.
func (m *JIDMap) Put(ctx context.Context, jid, sessionID string) error {
	const q = `
INSERT INTO jid_sessions (jid, session_id, created_at)
VALUES (?, ?, ?)
ON CONFLICT(jid) DO UPDATE SET session_id = excluded.session_id
`
	_, err := m.db.ExecContext(ctx, q, jid, sessionID,
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		return fmt.Errorf("sqlite: put jid: %w", err)
	}
	return nil
}
