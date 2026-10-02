package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

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

// EnsureSearch installs the search index (per-message and title FTS
// tables and their triggers). Open already does this; it is exported
// for callers that hold a Store through an interface. Idempotent.
func (s *Store) EnsureSearch(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, v2Schema); err != nil {
		return fmt.Errorf("sqlite: install search schema: %w", err)
	}
	return nil
}

// Search runs an FTS5 MATCH over message text and session titles and
// returns the best hit per session, ranked by bm25. query uses FTS5
// syntax (bare words, "phrases", prefix*).
func (s *Store) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchHit, error) {
	return s.search(ctx, "", query, opts, "sqlite: search")
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
	return s.search(ctx, sender, query, opts, "sqlite: search by sender")
}

func (s *Store) search(ctx context.Context, sender, query string, opts SearchOptions, op string) ([]SearchHit, error) {
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
	where, args := "", []any{query, query}
	if sender != "" {
		where, args = "AND s.sender = ?", append(args, sender)
	}
	// One candidate per matching message or title; ROW_NUMBER keeps
	// each session's best one.
	q := fmt.Sprintf(`
WITH hits AS (
    SELECT session_id, snippet(messages_fts, 2, '', '', '…', %d) AS snip, bm25(messages_fts) AS rank
    FROM messages_fts WHERE messages_fts MATCH ?
    UNION ALL
    SELECT session_id, title AS snip, bm25(titles_fts) AS rank
    FROM titles_fts WHERE titles_fts MATCH ?
), best AS (
    SELECT session_id, snip, rank,
           ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY rank) AS k
    FROM hits
)
SELECT b.session_id, s.title, b.snip, s.updated_at, b.rank
FROM best b JOIN sessions s ON s.id = b.session_id
WHERE b.k = 1 %s
ORDER BY b.rank
LIMIT %d
`, opts.SnippetChars/16, where, opts.Limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
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

// RecentSessions is a small helper that lists the N most recently
// touched sessions. Handy for CLI commands that render a picker.
func (s *Store) RecentSessions(ctx context.Context, limit int) ([]*agent.Session, error) {
	if limit == 0 {
		limit = 10
	}
	sums, err := s.List(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: recent: %w", err)
	}
	out := make([]*agent.Session, 0, len(sums))
	for _, sum := range sums {
		sess, err := s.Load(ctx, sum.ID)
		if err != nil {
			return nil, fmt.Errorf("sqlite: recent: %w", err)
		}
		out = append(out, sess)
	}
	return out, nil
}
