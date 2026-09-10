package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/reliability"
)

// reliabilitySamplesSchema mirrors the SQLite table (same seven
// columns) but uses Postgres-native types where it matters:
//
//   - TIMESTAMPTZ (not TEXT) for `at` so range queries pushdown
//     to an index scan instead of TEXT sort-compare.
//   - JSONB (not TEXT) for `metadata` so operators can filter in
//     SQL: `WHERE metadata @> '{"severity":"high"}'` for
//     alertmanager queries against the DB directly.
//   - BIGSERIAL PRIMARY KEY so LoadSince can order-by insertion
//     order without relying on a driver-specific rowid.
//
// Index on (at) covers the CLI's LoadSince(cutoff) scan. The
// PruneBefore statement uses the same index. No additional
// indexes today — labels-per-dimension are low-cardinality and
// full-table scans over a 30-day window (default retention) fit
// under a second at typical daemon throughput.
const reliabilitySamplesSchema = `
CREATE TABLE IF NOT EXISTS reliability_samples (
    id          BIGSERIAL PRIMARY KEY,
    at          TIMESTAMPTZ NOT NULL,
    dimension   TEXT NOT NULL,
    sub_metric  TEXT NOT NULL,
    value       DOUBLE PRECISION NOT NULL,
    session_id  TEXT NOT NULL DEFAULT '',
    bucket      TEXT NOT NULL DEFAULT '',
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_reliability_samples_at
    ON reliability_samples(at);
`

// ReliabilitySampleStore is the Postgres-backed twin of the
// SQLite implementation. Same [reliability.Recorder] +
// query surface, same fire-and-forget semantics, drop-in
// compatible so the daemon assembly's MultiRecorder wiring is
// the same regardless of driver.
type ReliabilitySampleStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewReliabilitySampleStore attaches to s and applies the schema.
// Idempotent — safe under HA replica startup. Logger may be nil.
func NewReliabilitySampleStore(ctx context.Context, s *Store, logger *slog.Logger) (*ReliabilitySampleStore, error) {
	if _, err := s.db.ExecContext(ctx, reliabilitySamplesSchema); err != nil {
		return nil, fmt.Errorf("postgres: apply reliability_samples schema: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ReliabilitySampleStore{db: s.db, logger: logger}, nil
}

// Record satisfies [reliability.Recorder]. Fire-and-forget INSERT;
// errors log at Warn and swallow.
func (r *ReliabilitySampleStore) Record(s reliability.Sample) {
	if r == nil || r.db == nil {
		return
	}
	meta := s.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	metaJSON, _ := json.Marshal(meta) //nolint:errcheck // string map, marshal is infallible
	at := s.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	const q = `
INSERT INTO reliability_samples
    (at, dimension, sub_metric, value, session_id, bucket, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
`
	_, err := r.db.ExecContext(context.Background(), q,
		at.UTC(),
		string(s.Dimension),
		s.SubMetric,
		s.Value,
		s.SessionID,
		s.Bucket,
		string(metaJSON),
	)
	if err != nil {
		r.logger.Warn("postgres: reliability sample insert",
			slog.String("dimension", string(s.Dimension)),
			slog.String("sub_metric", s.SubMetric),
			slog.String("err", err.Error()),
		)
	}
}

// LoadSince returns every sample with At >= cutoff, ordered by
// insertion (id). Metadata is decoded from JSONB back to
// map[string]string.
func (r *ReliabilitySampleStore) LoadSince(ctx context.Context, cutoff time.Time) ([]reliability.Sample, error) {
	const q = `
SELECT at, dimension, sub_metric, value, session_id, bucket, metadata::text
FROM reliability_samples
WHERE at >= $1
ORDER BY id ASC
`
	rs, err := r.db.QueryContext(ctx, q, cutoff.UTC())
	if err != nil {
		return nil, fmt.Errorf("postgres: reliability query: %w", err)
	}
	defer func() { _ = rs.Close() }() //nolint:errcheck // best-effort close

	var out []reliability.Sample
	for rs.Next() {
		var (
			at                              time.Time
			dim, sub, sess, bucket, metaStr string
			value                           float64
		)
		if err := rs.Scan(&at, &dim, &sub, &value, &sess, &bucket, &metaStr); err != nil {
			return out, fmt.Errorf("postgres: reliability scan: %w", err)
		}
		var meta map[string]string
		if metaStr != "" && metaStr != "{}" {
			if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
				r.logger.Debug("postgres: reliability skip bad metadata",
					slog.String("err", err.Error()))
				meta = nil
			}
		}
		out = append(out, reliability.Sample{
			At:        at,
			Dimension: reliability.Dimension(dim),
			SubMetric: sub,
			Value:     value,
			SessionID: sess,
			Bucket:    bucket,
			Metadata:  meta,
		})
	}
	if err := rs.Err(); err != nil {
		return out, fmt.Errorf("postgres: reliability rows: %w", err)
	}
	return out, nil
}

// PruneBefore removes samples older than cutoff. Called by the
// retention loop (reliability.RunPruner) every 6 hours by
// default.
func (r *ReliabilitySampleStore) PruneBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	const q = `DELETE FROM reliability_samples WHERE at < $1`
	res, err := r.db.ExecContext(ctx, q, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("postgres: reliability prune: %w", err)
	}
	n, _ := res.RowsAffected() //nolint:errcheck // postgres always reports
	return n, nil
}
