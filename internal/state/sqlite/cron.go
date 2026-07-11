package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const cronSchema = `
CREATE TABLE IF NOT EXISTS cron_jobs (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    cron_expr   TEXT NOT NULL,
    prompt      TEXT NOT NULL,
    deliver_to  TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    last_run_at TEXT
);
`

// CronJob is one scheduled prompt.
type CronJob struct {
	ID         string
	Name       string
	CronExpr   string
	Prompt     string
	DeliverTo  string
	Enabled    bool
	CreatedAt  time.Time
	LastRunAt  *time.Time
}

// CronStore persists scheduled prompts. All methods are safe for
// concurrent use.
type CronStore struct {
	db *sql.DB
}

// NewCronStore attaches a CronStore to an existing Store's database
// and installs the schema. Idempotent.
func NewCronStore(ctx context.Context, s *Store) (*CronStore, error) {
	if _, err := s.db.ExecContext(ctx, cronSchema); err != nil {
		return nil, fmt.Errorf("sqlite: install cron schema: %w", err)
	}
	return &CronStore{db: s.db}, nil
}

// Put inserts a new job. UNIQUE(name) prevents duplicates.
func (c *CronStore) Put(ctx context.Context, j CronJob) error {
	if j.ID == "" || j.Name == "" || j.CronExpr == "" || j.Prompt == "" {
		return errors.New("cron: id, name, cron_expr and prompt are required")
	}
	const q = `
INSERT INTO cron_jobs (id, name, cron_expr, prompt, deliver_to, enabled, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now().UTC()
	}
	_, err := c.db.ExecContext(ctx, q, j.ID, j.Name, j.CronExpr, j.Prompt, j.DeliverTo, boolInt(j.Enabled), fmtTime(j.CreatedAt))
	return err
}

// List returns every job newest-first.
func (c *CronStore) List(ctx context.Context) ([]CronJob, error) {
	const q = `
SELECT id, name, cron_expr, prompt, deliver_to, enabled, created_at, last_run_at
FROM cron_jobs ORDER BY created_at DESC`
	rows, err := c.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []CronJob
	for rows.Next() {
		var (
			job       CronJob
			enabled   int
			createdAt string
			lastRun   sql.NullString
		)
		if err := rows.Scan(&job.ID, &job.Name, &job.CronExpr, &job.Prompt, &job.DeliverTo, &enabled, &createdAt, &lastRun); err != nil {
			return nil, err
		}
		job.Enabled = enabled == 1
		job.CreatedAt = parseTime(createdAt)
		if lastRun.Valid {
			t := parseTime(lastRun.String)
			job.LastRunAt = &t
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// Delete removes a job by id.
func (c *CronStore) Delete(ctx context.Context, id string) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM cron_jobs WHERE id = ? OR name = ?`, id, id)
	return err
}

// SetEnabled toggles a job on or off without deleting it.
func (c *CronStore) SetEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := c.db.ExecContext(ctx, `UPDATE cron_jobs SET enabled = ? WHERE id = ? OR name = ?`, boolInt(enabled), id, id)
	return err
}

// RecordRun stamps a successful (or failed) execution.
func (c *CronStore) RecordRun(ctx context.Context, id string, at time.Time) error {
	_, err := c.db.ExecContext(ctx, `UPDATE cron_jobs SET last_run_at = ? WHERE id = ?`, fmtTime(at), id)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func fmtTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func parseTime(s string) time.Time {
	t, _ := time.Parse("2006-01-02T15:04:05.000Z", s)
	return t
}
