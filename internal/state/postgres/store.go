// Package postgres implements state.Store on top of PostgreSQL
// via the pgx stdlib driver. Purpose-built for the multi-replica
// HA story (ROADMAP §2.4): multiple rousseau-agent instances
// share session state through a single Postgres primary.
//
// # Scope
//
// This package implements ONLY the canonical [state.Store]
// interface (Save / Load / List / Delete / Close). The extension
// tables under internal/state/sqlite/ (cron scheduling, WhatsApp
// JID map, OAuth token cache, recall vectors, session cost
// ledger, session cache) stay SQLite-local for now — they were
// designed for a single-replica daemon and each needs its own
// review before porting. Sessions are the load-bearing HA
// concern; the extensions are per-daemon-instance state that a
// second replica can rebuild on its own.
//
// # Failure semantics
//
// The driver is a thin wrapper over pgx. Every method returns
// wrapped errors so callers can [errors.Is] them against
// [state.ErrNotFound] (for Load misses) or unwrap into
// pgx-native errors for logging. Connection pool tuning is left
// to [pgxpool] defaults through the stdlib bridge — the daemon's
// concurrency (~1 write per user message + occasional list) is
// well below the pool's headroom on any production Postgres.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" driver

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
)

//go:embed schema.sql
var schema string

// openDB is a testability seam over sql.Open. Production always uses
// sql.Open; unit tests stub it with a sqlmock-backed *sql.DB so the
// ping-failure and schema-apply-failure branches of Open are covered
// without a live Postgres. Mirrors the seam pattern used elsewhere in
// this codebase. Never reassigned outside tests.
var openDB = sql.Open

// Store is a state.Store backed by Postgres.
type Store struct {
	db *sql.DB
}

// Open opens a Postgres connection pool at dsn (a libpq-style
// URL, e.g. "postgres://user:pass@host:5432/rousseau?sslmode=require"),
// applies the schema, and returns a ready-to-use Store. The
// caller owns the returned Store and MUST call Close when done.
//
// A ping is performed before the schema apply so a bad DSN
// fails fast at boot instead of silently deferring the error to
// the first Save.
func Open(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("postgres: empty DSN")
	}
	db, err := openDB("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() //nolint:errcheck // constructor rollback; primary error already returned
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close() //nolint:errcheck // constructor rollback; primary error already returned
		return nil, fmt.Errorf("postgres: apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Save writes a Session, creating or replacing it. Uses ON
// CONFLICT DO UPDATE so the write path is race-free under
// concurrent replicas hitting the same session.
func (s *Store) Save(ctx context.Context, sess *agent.Session) error {
	payload, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("postgres: marshal session: %w", err)
	}
	const q = `
INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title,
    payload = EXCLUDED.payload,
    message_count = EXCLUDED.message_count,
    updated_at = EXCLUDED.updated_at
`
	_, err = s.db.ExecContext(ctx, q,
		sess.ID, sess.Title, string(payload), len(sess.Messages),
		sess.CreatedAt.UTC(), sess.UpdatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres: save session: %w", err)
	}
	return nil
}

// Load returns the Session identified by id, or state.ErrNotFound.
func (s *Store) Load(ctx context.Context, id string) (*agent.Session, error) {
	const q = `SELECT payload FROM sessions WHERE id = $1`
	var payload string
	err := s.db.QueryRowContext(ctx, q, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: load session: %w", err)
	}
	sess := &agent.Session{}
	if err := json.Unmarshal([]byte(payload), sess); err != nil {
		return nil, fmt.Errorf("postgres: unmarshal session: %w", err)
	}
	return sess, nil
}

// List returns Session summaries newest-first, capped at limit
// (0 disables the cap).
func (s *Store) List(ctx context.Context, limit int) ([]state.Summary, error) {
	// updated_at is stored as TIMESTAMPTZ; scan into time.Time and
	// format to match the SQLite driver's TEXT format so callers
	// don't have to switch on the driver.
	q := `SELECT id, title, message_count, updated_at FROM sessions ORDER BY updated_at DESC`
	var args []any
	if limit > 0 {
		q += ` LIMIT $1`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup on iteration completion

	var out []state.Summary
	for rows.Next() {
		var (
			sum       state.Summary
			updatedAt time.Time
		)
		if err := rows.Scan(&sum.ID, &sum.Title, &sum.MessageCount, &updatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan summary: %w", err)
		}
		// Match SQLite's ISO-8601 TEXT format so consumers don't
		// need to branch on the driver.
		sum.UpdatedAt = updatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate summaries: %w", err)
	}
	return out, nil
}

// Delete removes the Session identified by id. Deleting a
// missing Session is not an error (matches the SQLite driver).
func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete session: %w", err)
	}
	return nil
}

// Close releases the underlying pool.
func (s *Store) Close() error { return s.db.Close() }

// Compile-time interface satisfaction.
var _ state.Store = (*Store)(nil)
