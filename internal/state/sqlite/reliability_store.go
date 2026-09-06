package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/reliability"
)

// reliabilitySamplesSchema is the on-disk shape of the four-
// dimension decomposition's raw sample stream (arXiv:2602.16666).
// Every column is nullable except at, dimension, sub_metric, and
// value — the aggregator's own contract already treats the rest
// as optional.
//
// No composite index on (dimension, at) because typical query
// pattern is "last 7 / 30 days across all dimensions" (a full
// window scan is cheaper than an index on ≤10k rows). Revisit
// when a deployment retains millions of samples.
const reliabilitySamplesSchema = `
CREATE TABLE IF NOT EXISTS reliability_samples (
    at         TEXT NOT NULL,
    dimension  TEXT NOT NULL,
    sub_metric TEXT NOT NULL,
    value      REAL NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    bucket     TEXT NOT NULL DEFAULT '',
    metadata   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_reliability_samples_at
    ON reliability_samples(at);
`

// ReliabilitySampleStore is the SQLite-backed [reliability.Recorder]
// + query surface. Recording is a fire-and-forget INSERT — errors
// are logged at Warn and swallowed so telemetry can never wedge the
// agent's hot path. Reads (LoadSince) power the `rousseau
// reliability` CLI across process boundaries.
type ReliabilitySampleStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewReliabilitySampleStore attaches to s and applies the schema.
// Idempotent — safe to call from multiple daemon replicas sharing
// one DB file. logger may be nil; slog.Default() is used in that
// case for the fire-and-forget Warn on insert failures.
func NewReliabilitySampleStore(ctx context.Context, s *Store, logger *slog.Logger) (*ReliabilitySampleStore, error) {
	if _, err := s.db.ExecContext(ctx, reliabilitySamplesSchema); err != nil {
		return nil, fmt.Errorf("sqlite: apply reliability_samples schema: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ReliabilitySampleStore{db: s.db, logger: logger}, nil
}

// Record satisfies [reliability.Recorder]. Fire-and-forget: an
// INSERT failure logs a WARN and returns silently so a locked DB
// or ephemeral write error can never abort an agent turn.
func (r *ReliabilitySampleStore) Record(s reliability.Sample) {
	if r == nil || r.db == nil {
		return
	}
	metaJSON, _ := json.Marshal(s.Metadata) //nolint:errcheck // string map, marshal is infallible
	if s.Metadata == nil {
		metaJSON = []byte("")
	}
	const q = `
INSERT INTO reliability_samples
    (at, dimension, sub_metric, value, session_id, bucket, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?)
`
	at := s.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := r.db.ExecContext(context.Background(), q,
		at.UTC().Format(time.RFC3339Nano),
		string(s.Dimension),
		s.SubMetric,
		s.Value,
		s.SessionID,
		s.Bucket,
		string(metaJSON),
	)
	if err != nil {
		r.logger.Warn("sqlite: reliability sample insert",
			slog.String("dimension", string(s.Dimension)),
			slog.String("sub_metric", s.SubMetric),
			slog.String("err", err.Error()),
		)
	}
}

// LoadSince returns every sample with At >= cutoff. Rows are
// scanned in insertion order (ORDER BY rowid) so downstream
// aggregation is deterministic under equal timestamps.
//
// Callers should use this to reconstruct an in-memory aggregator
// at CLI startup — `rousseau reliability` loads the last 30d and
// asks the aggregator to summarize.
func (r *ReliabilitySampleStore) LoadSince(ctx context.Context, cutoff time.Time) ([]reliability.Sample, error) {
	const q = `
SELECT at, dimension, sub_metric, value, session_id, bucket, metadata
FROM reliability_samples
WHERE at >= ?
ORDER BY rowid ASC
`
	rows, err := r.db.QueryContext(ctx, q, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("sqlite: reliability query: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort close

	var out []reliability.Sample
	for rows.Next() {
		var (
			atStr, dim, sub, sess, bucket, metaStr string
			value                                  float64
		)
		if err := rows.Scan(&atStr, &dim, &sub, &value, &sess, &bucket, &metaStr); err != nil {
			return out, fmt.Errorf("sqlite: reliability scan: %w", err)
		}
		at, perr := time.Parse(time.RFC3339Nano, atStr)
		if perr != nil {
			// A malformed timestamp is bad, but we shouldn't drop
			// the entire query because of one bad row — skip and
			// log once at Debug.
			r.logger.Debug("sqlite: reliability skip bad timestamp",
				slog.String("at", atStr), slog.String("err", perr.Error()))
			continue
		}
		var meta map[string]string
		if metaStr != "" {
			if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
				r.logger.Debug("sqlite: reliability skip bad metadata",
					slog.String("err", err.Error()))
				// Continue with nil meta rather than skipping the row
				// — the value is the important part.
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
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("sqlite: reliability rows: %w", err)
	}
	return out, nil
}

// PruneBefore deletes samples with At < cutoff. Called from the
// existing daemon cron so the table doesn't grow unbounded. Returns
// the number of rows removed for logging.
func (r *ReliabilitySampleStore) PruneBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	const q = `DELETE FROM reliability_samples WHERE at < ?`
	res, err := r.db.ExecContext(ctx, q, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("sqlite: reliability prune: %w", err)
	}
	n, _ := res.RowsAffected() //nolint:errcheck // sqlite always reports
	return n, nil
}
