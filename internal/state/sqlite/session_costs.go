package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

const sessionCostsSchema = `
CREATE TABLE IF NOT EXISTS session_costs (
    session_id      TEXT NOT NULL,
    at              TEXT NOT NULL,           -- RFC3339 UTC
    provider        TEXT NOT NULL,
    model           TEXT NOT NULL,
    input_tokens    INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    cache_read      INTEGER NOT NULL DEFAULT 0,
    cache_creation  INTEGER NOT NULL DEFAULT 0,
    cost_usd        REAL    NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_session_costs_session_id_at
    ON session_costs(session_id, at DESC);

CREATE INDEX IF NOT EXISTS idx_session_costs_at
    ON session_costs(at DESC);
`

// SessionCostStore appends per-completion cost records to
// session_costs and answers grouping queries used by the
// `rousseau session cost` CLI + the `/cost` chat command.
type SessionCostStore struct {
	db *sql.DB
}

// NewSessionCostStore ensures the schema exists and returns a store
// that shares s's underlying *sql.DB. Idempotent — safe to call
// multiple times.
func NewSessionCostStore(ctx context.Context, s *Store) (*SessionCostStore, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sqlite: nil store")
	}
	if _, err := s.db.ExecContext(ctx, sessionCostsSchema); err != nil {
		return nil, fmt.Errorf("sqlite: apply session_costs schema: %w", err)
	}
	return &SessionCostStore{db: s.db}, nil
}

// Record persists one completion's cost record. Never blocks the
// caller on failure — Record returns an error that the caller can
// log at Warn, but the primary agent flow proceeds regardless.
func (c *SessionCostStore) Record(ctx context.Context, r CostRecord) error {
	const q = `
INSERT INTO session_costs
    (session_id, at, provider, model, input_tokens, output_tokens, cache_read, cache_creation, cost_usd)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`
	at := r.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err := c.db.ExecContext(ctx, q,
		r.SessionID,
		at.UTC().Format("2006-01-02T15:04:05.000Z"),
		r.Provider, r.Model,
		r.Usage.InputTokens, r.Usage.OutputTokens,
		r.Usage.CacheReadInputTokens, r.Usage.CacheCreationInputTokens,
		r.CostUSD,
	)
	if err != nil {
		return fmt.Errorf("sqlite: record cost: %w", err)
	}
	return nil
}

// SumBySession returns the aggregate cost + token counts for
// sessionID over the last since window. Zero-window means "all
// history". Missing session returns a zero-value Summary + nil.
func (c *SessionCostStore) SumBySession(ctx context.Context, sessionID string, since time.Duration) (Summary, error) {
	q := `
SELECT COALESCE(SUM(input_tokens),0),
       COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_read),0),
       COALESCE(SUM(cache_creation),0),
       COALESCE(SUM(cost_usd),0),
       COUNT(*)
FROM session_costs
WHERE session_id = ?
`
	args := []any{sessionID}
	if since > 0 {
		q += ` AND at >= ?`
		args = append(args, time.Now().UTC().Add(-since).Format("2006-01-02T15:04:05.000Z"))
	}
	var s Summary
	row := c.db.QueryRowContext(ctx, q, args...)
	if err := row.Scan(
		&s.InputTokens, &s.OutputTokens,
		&s.CacheReadTokens, &s.CacheCreationTokens,
		&s.CostUSD, &s.CompletionCount,
	); err != nil {
		return Summary{}, fmt.Errorf("sqlite: sum by session: %w", err)
	}
	return s, nil
}

// TopSessions returns the top-N sessions by cost over the last since
// window (zero = all history). Sessions with zero cost are excluded.
func (c *SessionCostStore) TopSessions(ctx context.Context, since time.Duration, limit int) ([]SessionRollup, error) {
	if limit <= 0 {
		limit = 25
	}
	q := `
SELECT session_id,
       COALESCE(SUM(cost_usd),0)         AS cost,
       COALESCE(SUM(input_tokens),0)     AS input,
       COALESCE(SUM(output_tokens),0)    AS output,
       COALESCE(SUM(cache_read),0)       AS cache_read,
       COALESCE(SUM(cache_creation),0)   AS cache_creation,
       COUNT(*)                           AS n
FROM session_costs
`
	args := []any{}
	if since > 0 {
		q += ` WHERE at >= ?`
		args = append(args, time.Now().UTC().Add(-since).Format("2006-01-02T15:04:05.000Z"))
	}
	q += ` GROUP BY session_id HAVING cost > 0 ORDER BY cost DESC LIMIT ?`
	args = append(args, limit)

	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: top sessions: %w", err)
	}
	defer rows.Close() //nolint:errcheck // best-effort cleanup

	var out []SessionRollup
	for rows.Next() {
		var r SessionRollup
		if err := rows.Scan(&r.SessionID, &r.CostUSD,
			&r.InputTokens, &r.OutputTokens,
			&r.CacheReadTokens, &r.CacheCreationTokens,
			&r.CompletionCount,
		); err != nil {
			return nil, fmt.Errorf("sqlite: scan top session: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate top sessions: %w", err)
	}
	return out, nil
}

// CostRecord is what callers hand to Record.
type CostRecord struct {
	SessionID string
	At        time.Time
	Provider  string
	Model     string
	Usage     agent.Usage
	CostUSD   float64
}

// Summary rolls up cost + token counts across N completions.
type Summary struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	CostUSD             float64
	CompletionCount     int
}

// SessionRollup is one row of a top-sessions listing.
type SessionRollup struct {
	SessionID           string
	CostUSD             float64
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	CompletionCount     int
}
