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
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: enable foreign keys: %w", err)
	}
	// busy_timeout: wait on lock contention instead of failing with
	// SQLITE_BUSY. Critical once concurrent transports (whatsapp today,
	// telegram/slack tomorrow) write into the same session store.
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=15000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: set busy_timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Save writes a Session, creating or replacing it.
func (s *Store) Save(ctx context.Context, sess *agent.Session) error {
	payload, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("sqlite: marshal session: %w", err)
	}
	const q = `
INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    title=excluded.title,
    payload=excluded.payload,
    message_count=excluded.message_count,
    updated_at=excluded.updated_at
`
	_, err = s.db.ExecContext(ctx, q,
		sess.ID, sess.Title, string(payload), len(sess.Messages),
		sess.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		sess.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
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
	defer func() { _ = rows.Close() }()

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

// Compile-time interface satisfaction check.
var _ state.Store = (*Store)(nil)
