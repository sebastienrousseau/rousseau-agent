package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqlitecosts "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// sessionCostsSchema is the Postgres-flavoured mirror of the
// SQLite schema. Differences from the SQLite version and why:
//
//   - `at TIMESTAMPTZ NOT NULL DEFAULT NOW()` instead of TEXT —
//     the SQLite driver formats time manually because SQLite has
//     no native timestamp type; Postgres does. Removes a formatting
//     round trip from Record.
//   - `cost_usd DOUBLE PRECISION` instead of REAL — REAL is 4-byte
//     in Postgres and would silently lose precision on aggregates.
//   - `BIGINT` instead of INTEGER for token counters — a single
//     long-running session can easily accumulate > 2^31 cache-read
//     tokens; the SQLite driver only survives on integer-affinity
//     luck.
//   - Composite index (session_id, at DESC) and single-column
//     index (at DESC) preserved — SumBySession + TopSessions with
//     a `since` window are the hot query shapes and both indexes
//     matter.
const sessionCostsSchema = `
CREATE TABLE IF NOT EXISTS session_costs (
    session_id     TEXT NOT NULL,
    at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    provider       TEXT NOT NULL,
    model          TEXT NOT NULL,
    input_tokens   BIGINT NOT NULL DEFAULT 0,
    output_tokens  BIGINT NOT NULL DEFAULT 0,
    cache_read     BIGINT NOT NULL DEFAULT 0,
    cache_creation BIGINT NOT NULL DEFAULT 0,
    cost_usd       DOUBLE PRECISION NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_session_costs_session_id_at
    ON session_costs(session_id, at DESC);

CREATE INDEX IF NOT EXISTS idx_session_costs_at
    ON session_costs(at DESC);
`

// CostRecord is the record callers hand to Record. Re-exported
// from the sqlite package so both drivers share a canonical shape.
type CostRecord = sqlitecosts.CostRecord

// Summary rolls up cost + token counts across N completions.
// Re-exported for the same reason as CostRecord.
type Summary = sqlitecosts.Summary

// SessionRollup is one row of a top-sessions listing.
// Re-exported for the same reason as CostRecord.
type SessionRollup = sqlitecosts.SessionRollup

// SessionCostStore appends per-completion cost records to
// session_costs and answers grouping queries used by the
// `rousseau session cost` CLI + the `/cost` chat command.
//
// Interface-compatible with [sqlitecosts.SessionCostStore] — same
// method signatures so the daemon can hold either concrete type
// without a shared interface (which would just re-declare what the
// two mirror).
type SessionCostStore struct {
	db *sql.DB
}

// NewSessionCostStore attaches to an existing Postgres [Store] and
// applies the schema. Idempotent — safe to call on every daemon
// boot.
func NewSessionCostStore(ctx context.Context, s *Store) (*SessionCostStore, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("postgres: nil store")
	}
	if _, err := s.db.ExecContext(ctx, sessionCostsSchema); err != nil {
		return nil, fmt.Errorf("postgres: apply session_costs schema: %w", err)
	}
	return &SessionCostStore{db: s.db}, nil
}

// Record persists one completion's cost record. Zero At is stamped
// server-side via DEFAULT NOW(); non-zero At is normalised to UTC
// before insert.
func (c *SessionCostStore) Record(ctx context.Context, r CostRecord) error {
	// Two variants so the DEFAULT NOW() clause can fire when the
	// caller left At zero. Sending NULL for at + relying on DEFAULT
	// keeps the wall-clock decision in one place (the DB).
	if r.At.IsZero() {
		const q = `
INSERT INTO session_costs
    (session_id, provider, model, input_tokens, output_tokens, cache_read, cache_creation, cost_usd)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
`
		_, err := c.db.ExecContext(ctx, q,
			r.SessionID, r.Provider, r.Model,
			r.Usage.InputTokens, r.Usage.OutputTokens,
			r.Usage.CacheReadInputTokens, r.Usage.CacheCreationInputTokens,
			r.CostUSD,
		)
		if err != nil {
			return fmt.Errorf("postgres: record cost: %w", err)
		}
		return nil
	}
	const q = `
INSERT INTO session_costs
    (session_id, at, provider, model, input_tokens, output_tokens, cache_read, cache_creation, cost_usd)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
`
	_, err := c.db.ExecContext(ctx, q,
		r.SessionID, r.At.UTC(), r.Provider, r.Model,
		r.Usage.InputTokens, r.Usage.OutputTokens,
		r.Usage.CacheReadInputTokens, r.Usage.CacheCreationInputTokens,
		r.CostUSD,
	)
	if err != nil {
		return fmt.Errorf("postgres: record cost: %w", err)
	}
	return nil
}

// SumBySession returns the aggregate cost + token counts for
// sessionID over the last since window. Zero-window means "all
// history". Missing session returns a zero-value Summary + nil —
// matches SQLite behaviour so callers do not need per-driver
// branches for the "unknown session" case.
func (c *SessionCostStore) SumBySession(ctx context.Context, sessionID string, since time.Duration) (Summary, error) {
	q := `
SELECT COALESCE(SUM(input_tokens),0),
       COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_read),0),
       COALESCE(SUM(cache_creation),0),
       COALESCE(SUM(cost_usd),0),
       COUNT(*)
FROM session_costs
WHERE session_id = $1
`
	args := []any{sessionID}
	if since > 0 {
		q += ` AND at >= $2`
		args = append(args, time.Now().UTC().Add(-since))
	}
	var s Summary
	row := c.db.QueryRowContext(ctx, q, args...)
	if err := row.Scan(
		&s.InputTokens, &s.OutputTokens,
		&s.CacheReadTokens, &s.CacheCreationTokens,
		&s.CostUSD, &s.CompletionCount,
	); err != nil {
		return Summary{}, fmt.Errorf("postgres: sum by session: %w", err)
	}
	return s, nil
}

// TopSessions returns the top-N sessions by cost over the last
// since window (zero = all history). Sessions with zero cost are
// excluded.
func (c *SessionCostStore) TopSessions(ctx context.Context, since time.Duration, limit int) ([]SessionRollup, error) {
	if limit <= 0 {
		limit = 25
	}
	q := `
SELECT session_id,
       COALESCE(SUM(cost_usd),0)        AS cost,
       COALESCE(SUM(input_tokens),0)    AS input,
       COALESCE(SUM(output_tokens),0)   AS output,
       COALESCE(SUM(cache_read),0)      AS cache_read,
       COALESCE(SUM(cache_creation),0)  AS cache_creation,
       COUNT(*)                         AS n
FROM session_costs
`
	args := []any{}
	next := 1
	if since > 0 {
		q += fmt.Sprintf(` WHERE at >= $%d`, next)
		args = append(args, time.Now().UTC().Add(-since))
		next++
	}
	q += fmt.Sprintf(` GROUP BY session_id HAVING SUM(cost_usd) > 0 ORDER BY cost DESC LIMIT $%d`, next)
	args = append(args, limit)

	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: top sessions: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []SessionRollup
	for rows.Next() {
		var r SessionRollup
		if err := rows.Scan(&r.SessionID, &r.CostUSD,
			&r.InputTokens, &r.OutputTokens,
			&r.CacheReadTokens, &r.CacheCreationTokens,
			&r.CompletionCount,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan top session: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate top sessions: %w", err)
	}
	return out, nil
}
