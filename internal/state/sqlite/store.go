// Package sqlite implements state.Store on top of SQLite via
// modernc.org/sqlite (pure Go — no CGO required).
package sqlite

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"database/sql"

	_ "modernc.org/sqlite" // register driver

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
)

//go:embed schema.sql
var schema string

// fileDSN adds per-connection pragmas to a file path. database/sql
// pools connections, and a PRAGMA run through db.Exec reaches only the
// connection that executed it; every other connection would run with
// busy_timeout=0 (immediate SQLITE_BUSY under contention) and foreign
// keys off. modernc.org/sqlite applies _pragma parameters on each new
// connection. ":memory:", existing URIs, and paths containing URI
// delimiters ('?', '#') are left as given; the latter keep the old
// single-connection pragmas rather than risk a mis-parsed path.
func fileDSN(path string) string {
	if path == ":memory:" || strings.HasPrefix(path, "file:") || strings.ContainsAny(path, "?#") {
		return path
	}
	return "file:" + path +
		"?_pragma=busy_timeout(15000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)"
}

// Store is a state.Store backed by SQLite.
type Store struct {
	// path is the database file Open was given (":memory:" or a
	// file: URI included); erasure reports backups next to it.
	path string
	db   *sql.DB
}

// Open opens (or creates) a SQLite database at path and applies the
// schema. Pass ":memory:" for an in-process database.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", fileDSN(path))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	if path == ":memory:" {
		// Every pooled connection to :memory: is a separate, empty
		// database; one connection keeps them all on the same one.
		db.SetMaxOpenConns(1)
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
	if err := ensureColumn(ctx, db, "search_text", `ALTER TABLE sessions ADD COLUMN search_text TEXT NOT NULL DEFAULT ''`); err != nil {
		db.Close() //nolint:errcheck // constructor rollback; primary error is already being returned
		return nil, err
	}
	s := &Store{db: db, path: path}
	if err := s.ensureVersion(ctx); err != nil {
		db.Close() //nolint:errcheck // constructor rollback; primary error is already being returned
		return nil, err
	}
	return s, nil
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

// ensureColumn adds a column to sessions when it is missing.
func ensureColumn(ctx context.Context, db *sql.DB, name, ddl string) error {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = ?`, name).Scan(&n); err != nil {
		return fmt.Errorf("sqlite: probe column %s: %w", name, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("sqlite: add column %s: %w", name, err)
	}
	return nil
}

// searchText is what the full-text index holds for a session: the
// text of its messages, one per line. The JSON payload (keys, base64
// image bytes, tool-call scaffolding) is deliberately left out.
func searchText(sess *model.Session) string {
	var b strings.Builder
	for _, m := range sess.Messages {
		for _, c := range m.Content {
			if c.Kind == model.ContentText && c.Text != "" {
				b.WriteString(c.Text)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
