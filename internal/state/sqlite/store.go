// Package sqlite implements state.Store on top of SQLite via
// modernc.org/sqlite (pure Go — no CGO required).
package sqlite

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"database/sql"

	_ "modernc.org/sqlite" // register driver

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
)

//go:embed schema.sql
var schema string

// Store is a state.Store backed by SQLite.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at path and applies the
// schema. Pass ":memory:" for an in-process database.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		db.Close() //nolint:errcheck // constructor rollback; primary error is already being returned
		return nil, fmt.Errorf("sqlite: enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		db.Close() //nolint:errcheck // constructor rollback; primary error is already being returned
		return nil, fmt.Errorf("sqlite: enable foreign keys: %w", err)
	}
	// busy_timeout: wait on lock contention instead of failing with
	// SQLITE_BUSY. Critical once concurrent transports (whatsapp today,
	// telegram/slack tomorrow) write into the same session store.
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=15000"); err != nil {
		db.Close() //nolint:errcheck // constructor rollback; primary error is already being returned
		return nil, fmt.Errorf("sqlite: set busy_timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close() //nolint:errcheck // constructor rollback; primary error is already being returned
		return nil, fmt.Errorf("sqlite: apply schema: %w", err)
	}
	// Runtime migration: pre-lifecycle-verbs databases lack the
	// sender column on sessions. SQLite has no IF NOT EXISTS on
	// ALTER TABLE ADD COLUMN, so we probe via PRAGMA table_info
	// and add the column + index only when missing. Idempotent
	// on every open.
	if err := ensureSenderColumn(ctx, db); err != nil {
		db.Close() //nolint:errcheck // constructor rollback; primary error is already being returned
		return nil, err
	}
	s := &Store{db: db}
	if err := s.EnsureSearch(ctx); err != nil {
		db.Close() //nolint:errcheck // constructor rollback; primary error is already being returned
		return nil, err
	}
	return s, nil
}

// Save writes a Session, creating or replacing it.
func (s *Store) Save(ctx context.Context, sess *agent.Session) error {
	payload, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("sqlite: marshal session: %w", err)
	}
	// sender is persisted both in the payload (for round-trip via
	// Load's json.Unmarshal) and in a top-level column so
	// ListBySender can index-scan without decoding every row.
	const q = `
INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at, sender)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    title=excluded.title,
    payload=excluded.payload,
    message_count=excluded.message_count,
    updated_at=excluded.updated_at,
    sender=excluded.sender
`
	_, err = s.db.ExecContext(ctx, q,
		sess.ID, sess.Title, string(payload), len(sess.Messages),
		sess.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		sess.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		sess.Sender,
	)
	if err != nil {
		return fmt.Errorf("sqlite: save session: %w", err)
	}
	return nil
}

// Load returns the Session identified by id, or state.ErrNotFound.
func (s *Store) Load(ctx context.Context, id string) (*agent.Session, error) {
	const q = `SELECT payload FROM sessions WHERE id = ?`
	var payload string
	err := s.db.QueryRowContext(ctx, q, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: load session: %w", err)
	}
	sess := &agent.Session{}
	if err := json.Unmarshal([]byte(payload), sess); err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal session: %w", err)
	}
	return sess, nil
}

// List returns Session summaries newest-first, capped at limit.
func (s *Store) List(ctx context.Context, limit int) ([]state.Summary, error) {
	q := `SELECT id, title, message_count, updated_at FROM sessions ORDER BY updated_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup on iteration completion

	var out []state.Summary
	for rows.Next() {
		var sum state.Summary
		if err := rows.Scan(&sum.ID, &sum.Title, &sum.MessageCount, &sum.UpdatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan summary: %w", err)
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate summaries: %w", err)
	}
	return out, nil
}

// Delete removes the Session identified by id.
func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: delete session: %w", err)
	}
	return nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// ListBySender returns Session summaries for the given sender,
// newest-first, capped at limit (0 disables the cap).
//
// Used by the transport-side /sessions verb — the sender is
// the JID / handle that typed the command, and the list must
// only include sessions belonging to that sender (never any
// other JID's sessions, and never legacy sessions with empty
// sender). Enforced by the WHERE clause so no application-
// level filtering is needed.
func (s *Store) ListBySender(ctx context.Context, sender string, limit int) ([]state.Summary, error) {
	if sender == "" {
		// A per-sender query keyed on the empty string would
		// return every legacy row — the opposite of what the
		// caller wants. Return empty explicitly.
		return nil, nil
	}
	q := `SELECT id, title, message_count, updated_at FROM sessions WHERE sender = ? ORDER BY updated_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q, sender)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list sessions by sender: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []state.Summary
	for rows.Next() {
		var sum state.Summary
		if err := rows.Scan(&sum.ID, &sum.Title, &sum.MessageCount, &sum.UpdatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan summary: %w", err)
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate summaries: %w", err)
	}
	return out, nil
}

// ensureSenderColumn adds the sender column (when missing) and
// its index to the sessions table. Runs on every Open —
// idempotent by design:
//
//   - Fresh install: sessions.CREATE TABLE in schema.sql already
//     includes the sender column, so the ADD COLUMN branch is
//     skipped. The CREATE INDEX runs and finds an empty index
//     to bootstrap.
//   - Legacy install (pre-lifecycle-verbs): schema.sql's
//     CREATE TABLE IF NOT EXISTS no-ops on the existing table.
//     PRAGMA table_info(sessions) shows no sender column, so
//     we ADD COLUMN then CREATE INDEX.
//
// SQLite has no IF NOT EXISTS on ALTER TABLE ADD COLUMN, so the
// PRAGMA probe is the only portable way to make the migration
// idempotent. CREATE INDEX IF NOT EXISTS handles its own
// idempotency in both branches so it is safe to run
// unconditionally.
//
// The index cannot live in schema.sql because a legacy DB
// without the sender column would fail the CREATE INDEX at
// schema-apply time — before this migration has a chance to
// add the column.
func ensureSenderColumn(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(sessions)`)
	if err != nil {
		return fmt.Errorf("sqlite: probe sessions columns: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup
	haveSender := false
	for rows.Next() {
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dflt        sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("sqlite: scan sessions column info: %w", err)
		}
		if name == "sender" {
			haveSender = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite: iterate sessions column info: %w", err)
	}
	if !haveSender {
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE sessions ADD COLUMN sender TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("sqlite: add sender column: %w", err)
		}
	}
	// Always safe: CREATE INDEX IF NOT EXISTS is a no-op if the
	// index already exists, and both branches above guarantee
	// the sender column is present when we reach here.
	if _, err := db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_sessions_sender_updated ON sessions(sender, updated_at DESC)`); err != nil {
		return fmt.Errorf("sqlite: index sender: %w", err)
	}
	return nil
}

// Compile-time interface satisfaction check.
var _ state.Store = (*Store)(nil)
