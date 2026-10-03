package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// searchSchema installs an FTS5 virtual table plus triggers that keep
// it in sync with the sessions table. Idempotent.
const searchSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS sessions_fts USING fts5(
    session_id UNINDEXED,
    title,
    body,
    tokenize = 'porter unicode61'
);

CREATE TRIGGER IF NOT EXISTS sessions_fts_ai AFTER INSERT ON sessions BEGIN
    INSERT INTO sessions_fts (session_id, title, body)
    VALUES (NEW.id, NEW.title, NEW.search_text);
END;

CREATE TRIGGER IF NOT EXISTS sessions_fts_au AFTER UPDATE ON sessions BEGIN
    DELETE FROM sessions_fts WHERE session_id = OLD.id;
    INSERT INTO sessions_fts (session_id, title, body)
    VALUES (NEW.id, NEW.title, NEW.search_text);
END;

CREATE TRIGGER IF NOT EXISTS sessions_fts_ad AFTER DELETE ON sessions BEGIN
    DELETE FROM sessions_fts WHERE session_id = OLD.id;
END;
`

// SearchHit is one row of a full-text search result.
type SearchHit struct {
	SessionID string
	Title     string
	Snippet   string
	UpdatedAt time.Time
	// Rank is FTS5's bm25 score (lower is more relevant); provided so
	// callers can sort or expose it in UIs.
	Rank float64
}

// SearchOptions tunes a search.
type SearchOptions struct {
	// Limit caps returned hits. Zero uses 20.
	Limit int
	// SnippetChars is the target snippet length in characters. Zero
	// uses 200.
	SnippetChars int
}

// EnsureSearch backfills the FTS index for any sessions that predate
// the search schema, then installs the schema + triggers if missing.
// Safe to call every time the Store opens.
func (s *Store) EnsureSearch(ctx context.Context) error {
	if err := s.migrateSearchText(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, searchSchema); err != nil {
		return fmt.Errorf("sqlite: install search schema: %w", err)
	}
	const backfill = `
INSERT INTO sessions_fts (session_id, title, body)
SELECT s.id, s.title, s.search_text
FROM sessions s
LEFT JOIN sessions_fts f ON f.session_id = s.id
WHERE f.session_id IS NULL
`
	if _, err := s.db.ExecContext(ctx, backfill); err != nil {
		return fmt.Errorf("sqlite: backfill fts: %w", err)
	}
	return nil
}

// Search runs an FTS5 query and returns ranked hits.
//
// The query string is passed to FTS5 as-is; callers wanting phrase
// searches or boolean operators can use FTS5's native syntax
// (e.g. `"kubernetes" OR helm`).
func (s *Store) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("sqlite: empty search query")
	}
	if opts.Limit == 0 {
		opts.Limit = 20
	}
	if opts.SnippetChars == 0 {
		opts.SnippetChars = 200
	}
	q := fmt.Sprintf(`
SELECT
    f.session_id,
    s.title,
    snippet(sessions_fts, 2, '', '', '…', %d) AS snippet,
    s.updated_at,
    bm25(sessions_fts) AS rank
FROM sessions_fts f
JOIN sessions s ON s.id = f.session_id
WHERE sessions_fts MATCH ?
ORDER BY rank
LIMIT %d
`, opts.SnippetChars/16, opts.Limit)

	rows, err := s.db.QueryContext(ctx, q, query)
	if err != nil {
		return nil, fmt.Errorf("sqlite: search: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup on iteration completion

	var out []SearchHit
	for rows.Next() {
		var (
			hit       SearchHit
			updatedAt string
		)
		if err := rows.Scan(&hit.SessionID, &hit.Title, &hit.Snippet, &updatedAt, &hit.Rank); err != nil {
			return nil, fmt.Errorf("sqlite: scan hit: %w", err)
		}
		if t, err := time.Parse("2006-01-02T15:04:05.000Z", updatedAt); err == nil {
			hit.UpdatedAt = t
		}
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate hits: %w", err)
	}
	return out, nil
}

// SearchBySender runs Search but restricts hits to sessions
// whose sender matches the caller's JID. Backs the transport
// /find chat verb — every operator only sees their own
// conversation history. Empty sender short-circuits to nil so
// a bug that forgot to plumb the sender through never
// accidentally leaks cross-sender content.
func (s *Store) SearchBySender(ctx context.Context, sender, query string, opts SearchOptions) ([]SearchHit, error) {
	if sender == "" {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("sqlite: empty search query")
	}
	if opts.Limit == 0 {
		opts.Limit = 20
	}
	if opts.SnippetChars == 0 {
		opts.SnippetChars = 200
	}
	q := fmt.Sprintf(`
SELECT
    f.session_id,
    s.title,
    snippet(sessions_fts, 2, '', '', '…', %d) AS snippet,
    s.updated_at,
    bm25(sessions_fts) AS rank
FROM sessions_fts f
JOIN sessions s ON s.id = f.session_id
WHERE sessions_fts MATCH ? AND s.sender = ?
ORDER BY rank
LIMIT %d
`, opts.SnippetChars/16, opts.Limit)

	rows, err := s.db.QueryContext(ctx, q, query, sender)
	if err != nil {
		return nil, fmt.Errorf("sqlite: search by sender: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []SearchHit
	for rows.Next() {
		var (
			hit       SearchHit
			updatedAt string
		)
		if err := rows.Scan(&hit.SessionID, &hit.Title, &hit.Snippet, &updatedAt, &hit.Rank); err != nil {
			return nil, fmt.Errorf("sqlite: scan hit: %w", err)
		}
		if t, err := time.Parse("2006-01-02T15:04:05.000Z", updatedAt); err == nil {
			hit.UpdatedAt = t
		}
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate hits: %w", err)
	}
	return out, nil
}

// RecentSessions is a small helper that lists the N most recently
// touched sessions. Handy for CLI commands that render a picker.
func (s *Store) RecentSessions(ctx context.Context, limit int) ([]*agent.Session, error) {
	if limit == 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT payload FROM sessions ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: recent: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup on iteration completion

	var out []*agent.Session
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		sess := &agent.Session{}
		if err := json.Unmarshal([]byte(payload), sess); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal session: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// searchSchemaVersion is the PRAGMA user_version once the FTS index
// holds extracted message text rather than raw JSON payloads.
const searchSchemaVersion = 1

// migrateSearchText upgrades a database whose FTS index was built from
// raw payloads (JSON keys, base64 images): it fills search_text for
// every session, drops the payload-indexing triggers so searchSchema
// recreates them on search_text, and rebuilds the index. Runs once;
// later opens see user_version and skip it.
func (s *Store) migrateSearchText(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("sqlite: read user_version: %w", err)
	}
	if version >= searchSchemaVersion {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, payload FROM sessions`)
	if err != nil {
		return fmt.Errorf("sqlite: search migration: list: %w", err)
	}
	type update struct{ id, text string }
	var updates []update
	for rows.Next() {
		var id, payload string
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close() //nolint:errcheck,gosec // primary error is returned
			return fmt.Errorf("sqlite: search migration: scan: %w", err)
		}
		var sess agent.Session
		if json.Unmarshal([]byte(payload), &sess) == nil {
			updates = append(updates, update{id, searchText(&sess)})
		}
	}
	rows.Close() //nolint:errcheck,gosec // iteration finished
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite: search migration: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: search migration: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for _, q := range []string{
		`DROP TRIGGER IF EXISTS sessions_fts_ai`,
		`DROP TRIGGER IF EXISTS sessions_fts_au`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("sqlite: search migration: %w", err)
		}
	}
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET search_text = ? WHERE id = ?`, u.text, u.id); err != nil {
			return fmt.Errorf("sqlite: search migration: backfill: %w", err)
		}
	}
	var haveFTS int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'sessions_fts'`).Scan(&haveFTS); err != nil {
		return fmt.Errorf("sqlite: search migration: %w", err)
	}
	if haveFTS > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions_fts`); err != nil {
			return fmt.Errorf("sqlite: search migration: clear index: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, searchSchemaVersion)); err != nil {
		return fmt.Errorf("sqlite: search migration: version: %w", err)
	}
	return tx.Commit()
}
