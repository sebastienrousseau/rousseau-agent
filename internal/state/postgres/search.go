package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitesearch "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// SearchHit is re-exported from the sqlite package so the
// canonical shape is shared across drivers. Same fields, same
// semantics — Rank is ts_rank_cd on Postgres vs. FTS5 bm25 on
// SQLite, both "higher magnitude means less relevant" contract
// but the number itself is not portable. Callers should treat
// Rank as an ordering key, not a semantic score.
type SearchHit = sqlitesearch.SearchHit

// SearchOptions is re-exported for the same reason as SearchHit.
type SearchOptions = sqlitesearch.SearchOptions

// EnsureSearch installs the search index: a generated tsvector over
// each message's text (session_messages.body_vector) and over each
// session's title (sessions.title_vector), both GIN-indexed. Open
// already does this; it is exported for callers holding a Store
// through an interface. Idempotent. Language 'english' matches the
// SQLite driver's porter stemmer.
func (s *Store) EnsureSearch(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, v2Schema); err != nil {
		return fmt.Errorf("postgres: install search schema: %w", err)
	}
	return nil
}

// Search runs a websearch_to_tsquery over message text and session
// titles and returns the best hit per session, highest rank first.
func (s *Store) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchHit, error) {
	return s.search(ctx, "", query, opts, "postgres: search")
}

// SearchBySender is Search restricted to sessions whose sender is the
// caller's key. Empty sender returns nil so a caller that forgot to
// plumb the sender through never sees other senders' history.
func (s *Store) SearchBySender(ctx context.Context, sender, query string, opts SearchOptions) ([]SearchHit, error) {
	if sender == "" {
		return nil, nil
	}
	return s.search(ctx, sender, query, opts, "postgres: search by sender")
}

func (s *Store) search(ctx context.Context, sender, query string, opts SearchOptions, op string) ([]SearchHit, error) {
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
	maxWords := max(opts.SnippetChars/6, 2)
	minWords := max(maxWords/2, 1)
	headlineOpts := fmt.Sprintf(
		`MaxWords=%d, MinWords=%d, ShortWord=3, HighlightAll=false, StartSel="", StopSel=""`,
		maxWords, minWords,
	)
	// One candidate per matching message or title; DISTINCT ON keeps
	// each session's best one.
	const q = `
WITH hits AS (
    SELECT m.session_id, m.body AS text, ts_rank_cd(m.body_vector, websearch_to_tsquery('english', $1)) AS rank
    FROM session_messages m
    WHERE m.body_vector @@ websearch_to_tsquery('english', $1)
    UNION ALL
    SELECT s.id, s.title, ts_rank_cd(s.title_vector, websearch_to_tsquery('english', $1))
    FROM sessions s
    WHERE s.title_vector @@ websearch_to_tsquery('english', $1)
), best AS (
    SELECT DISTINCT ON (session_id) session_id, text, rank
    FROM hits
    ORDER BY session_id, rank DESC
)
SELECT b.session_id, s.title,
       ts_headline('english', b.text, websearch_to_tsquery('english', $1), $3) AS snippet,
       s.updated_at, b.rank
FROM best b JOIN sessions s ON s.id = b.session_id
WHERE $4 = '' OR s.sender = $4
ORDER BY b.rank DESC
LIMIT $2
`
	rows, err := s.db.QueryContext(ctx, q, query, opts.Limit, headlineOpts, sender)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
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
	sums, err := s.List(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: recent: %w", err)
	}
	out := make([]*agent.Session, 0, len(sums))
	for _, sum := range sums {
		sess, err := s.Load(ctx, sum.ID)
		if err != nil {
			return nil, fmt.Errorf("postgres: recent: %w", err)
		}
		out = append(out, sess)
	}
	return out, nil
}
