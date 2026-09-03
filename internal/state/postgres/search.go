package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitesearch "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// searchSchema adds a Postgres full-text search index on top of
// the sessions table. Idempotent — safe to call every daemon
// boot. Differences from the SQLite driver's FTS5 setup and why:
//
//   - Generated column `search_vector tsvector` derived from
//     title + payload. Postgres 12+ handles updates automatically
//     — no INSERT / UPDATE / DELETE triggers needed. The SQLite
//     driver installs three triggers because FTS5 is a separate
//     virtual table; here the index lives on the same row.
//   - GIN index on the generated column so `@@` queries hit an
//     index scan. Without it, every search is a seq scan of
//     to_tsvector(payload) — fine at 100 rows, unacceptable at
//     100k.
//   - Language `'english'` matches the FTS5 `porter unicode61`
//     stemmer choice — same "walk / walking / walked" collapse
//     an operator would get on SQLite. Operators pinning a
//     different language should replace this constant in a
//     follow-up (not exposed as config yet — no concrete demand).
const searchSchema = `
ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(title, '')),   'A') ||
        setweight(to_tsvector('english', coalesce(payload, '')), 'B')
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_sessions_search_vector
    ON sessions USING GIN (search_vector);
`

// SearchHit is re-exported from the sqlite package so the
// canonical shape is shared across drivers. Same fields, same
// semantics — Rank is ts_rank_cd on Postgres vs. FTS5 bm25 on
// SQLite, both "higher magnitude means less relevant" contract
// but the number itself is not portable. Callers should treat
// Rank as an ordering key, not a semantic score.
type SearchHit = sqlitesearch.SearchHit

// SearchOptions is re-exported for the same reason as SearchHit
// — one canonical option surface across drivers.
type SearchOptions = sqlitesearch.SearchOptions

// EnsureSearch installs the search schema (generated tsvector
// column + GIN index). Called by daemon startup once the sessions
// table is known to exist. Mirrors [sqlitesearch.EnsureSearch]'s
// contract: idempotent, safe on every open, no-op if the schema
// is already in place.
func (s *Store) EnsureSearch(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, searchSchema); err != nil {
		return fmt.Errorf("postgres: install search schema: %w", err)
	}
	return nil
}

// Search runs a Postgres full-text query and returns ranked hits.
// Query string is parsed with `websearch_to_tsquery` so a caller
// can pass a natural query like `postgres OR mysql -pgvector` and
// it will parse the same as they'd expect from Google.
//
// Matches the SQLite driver's Search contract:
//   - empty query → error (not empty result — that would mask a
//     caller bug)
//   - default limit 20
//   - default snippet 200 chars
//   - hits sorted by rank (best match first)
func (s *Store) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("postgres: empty search query")
	}
	if opts.Limit == 0 {
		opts.Limit = 20
	}
	if opts.SnippetChars == 0 {
		opts.SnippetChars = 200
	}

	// ts_headline is expensive (re-tokenises the source text), so
	// it runs only over the LIMIT rows via a subquery. Doing it
	// in the outer projection would headline every candidate row
	// before the ORDER + LIMIT collapse; on a 100k-row table
	// that is the difference between ms and seconds.
	//
	// MaxWords is derived from SnippetChars/6 to loosely match
	// English average word length + one space per word — matches
	// the SQLite driver's chars/16-ish word-per-fragment shape.
	// Options are formatted server-side rather than parameterised
	// individually because pg's ts_headline reads them as a single
	// comma-separated string, not as bound values.
	maxWords := opts.SnippetChars / 6
	if maxWords < 2 {
		maxWords = 2
	}
	minWords := maxWords / 2
	if minWords < 1 {
		minWords = 1
	}
	// StartSel / StopSel are quoted-empty so ts_headline returns
	// plain text without <b>...</b> wrappers — matches the SQLite
	// driver's snippet() call which uses empty delimiters. Bare
	// `StartSel=,` (unquoted empty) is a SQLSTATE 42601 syntax
	// error in ts_headline's option parser; the quotes make it
	// happy.
	headlineOpts := fmt.Sprintf(
		`MaxWords=%d, MinWords=%d, ShortWord=3, HighlightAll=false, StartSel="", StopSel=""`,
		maxWords, minWords,
	)
	const q = `
WITH ranked AS (
    SELECT id, title, payload, updated_at,
           ts_rank_cd(search_vector, websearch_to_tsquery('english', $1)) AS rank
    FROM sessions
    WHERE search_vector @@ websearch_to_tsquery('english', $1)
    ORDER BY rank DESC
    LIMIT $2
)
SELECT id, title,
       ts_headline('english', payload,
                   websearch_to_tsquery('english', $1),
                   $3) AS snippet,
       updated_at,
       rank
FROM ranked
ORDER BY rank DESC
`
	rows, err := s.db.QueryContext(ctx, q, query, opts.Limit, headlineOpts)
	if err != nil {
		return nil, fmt.Errorf("postgres: search: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []SearchHit
	for rows.Next() {
		var (
			hit       SearchHit
			updatedAt sql.NullTime
		)
		if err := rows.Scan(&hit.SessionID, &hit.Title, &hit.Snippet, &updatedAt, &hit.Rank); err != nil {
			return nil, fmt.Errorf("postgres: scan hit: %w", err)
		}
		if updatedAt.Valid {
			hit.UpdatedAt = updatedAt.Time.UTC()
		}
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate hits: %w", err)
	}
	return out, nil
}

// RecentSessions returns the N most recently touched sessions.
// Handy for CLI commands that render a picker. Matches the
// SQLite driver's helper so `rousseau session list` can talk
// to either driver.
func (s *Store) RecentSessions(ctx context.Context, limit int) ([]*agent.Session, error) {
	if limit == 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT payload FROM sessions ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: recent: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []*agent.Session
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		sess := &agent.Session{}
		if err := json.Unmarshal([]byte(payload), sess); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal session: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}
