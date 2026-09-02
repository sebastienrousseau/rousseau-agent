package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	sqlitecron "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// cronSchema is the Postgres-flavoured mirror of the SQLite
// schema. Differences from the SQLite version and why:
//
//   - `enabled BOOLEAN` instead of INTEGER-with-0/1 — Postgres
//     has a real bool type; keeping SQLite semantics would just
//     force awkward casts every query.
//   - `created_at TIMESTAMPTZ` / `last_run_at TIMESTAMPTZ NULL`
//     instead of TEXT — same reasoning as the sessions table
//     in postgres/store.go: native time types give proper range
//     queries + index usage.
//   - Same TEXT PRIMARY KEY on id (session-style UUIDs) and
//     TEXT UNIQUE on name so operator-visible identifiers stay
//     identical across drivers.
const cronSchema = `
CREATE TABLE IF NOT EXISTS cron_jobs (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    cron_expr   TEXT NOT NULL,
    prompt      TEXT NOT NULL,
    deliver_to  TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL,
    last_run_at TIMESTAMPTZ NULL
);
`

// CronJob is the domain type. Re-exported from
// internal/state/sqlite so callers can accept either driver's
// CronStore without type-swapping. Keeps a single canonical
// shape while permitting per-driver storage encoding.
type CronJob = sqlitecron.CronJob

// CronStore is a Postgres-backed cron-job repository.
// Interface-compatible with [sqlitecron.CronStore] — every
// method has the same signature so the daemon can hold either
// concrete type without a shared interface (which would just
// re-declare what the two mirror anyway).
type CronStore struct {
	db *sql.DB
}

// NewCronStore attaches to an existing Postgres [Store] and
// applies the schema. Idempotent — safe to call on every
// daemon boot.
func NewCronStore(ctx context.Context, s *Store) (*CronStore, error) {
	if _, err := s.db.ExecContext(ctx, cronSchema); err != nil {
		return nil, fmt.Errorf("postgres: install cron schema: %w", err)
	}
	return &CronStore{db: s.db}, nil
}

// Put inserts a new job. UNIQUE(name) prevents duplicates.
// Mirrors the SQLite driver's validation so operators see the
// same error surface whichever driver they use.
func (c *CronStore) Put(ctx context.Context, j CronJob) error {
	if j.ID == "" || j.Name == "" || j.CronExpr == "" || j.Prompt == "" {
		return errors.New("cron: id, name, cron_expr and prompt are required")
	}
	const q = `
INSERT INTO cron_jobs (id, name, cron_expr, prompt, deliver_to, enabled, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now().UTC()
	}
	_, err := c.db.ExecContext(ctx, q,
		j.ID, j.Name, j.CronExpr, j.Prompt, j.DeliverTo, j.Enabled, j.CreatedAt.UTC(),
	)
	return err
}

// List returns every job newest-first.
func (c *CronStore) List(ctx context.Context) ([]CronJob, error) {
	const q = `
SELECT id, name, cron_expr, prompt, deliver_to, enabled, created_at, last_run_at
FROM cron_jobs ORDER BY created_at DESC
`
	rows, err := c.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []CronJob
	for rows.Next() {
		var (
			job     CronJob
			lastRun sql.NullTime
		)
		if err := rows.Scan(
			&job.ID, &job.Name, &job.CronExpr, &job.Prompt, &job.DeliverTo,
			&job.Enabled, &job.CreatedAt, &lastRun,
		); err != nil {
			return nil, err
		}
		if lastRun.Valid {
			t := lastRun.Time.UTC()
			job.LastRunAt = &t
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// Delete removes a job by ID OR by name.
func (c *CronStore) Delete(ctx context.Context, id string) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM cron_jobs WHERE id = $1 OR name = $1`, id)
	return err
}

// SetEnabled toggles a job on or off without deleting it.
func (c *CronStore) SetEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := c.db.ExecContext(ctx, `UPDATE cron_jobs SET enabled = $2 WHERE id = $1 OR name = $1`, id, enabled)
	return err
}

// RecordRun stamps the last successful (or failed) execution.
func (c *CronStore) RecordRun(ctx context.Context, id string, at time.Time) error {
	_, err := c.db.ExecContext(ctx, `UPDATE cron_jobs SET last_run_at = $1 WHERE id = $2`, at.UTC(), id)
	return err
}
