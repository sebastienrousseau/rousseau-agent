package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// recallVectorsSchema stores per-chunk metadata + a binary embedding
// blob. session_id + message_id + chunk_index is the natural key; the
// unique index prevents double-insertion when re-embedding.
const recallVectorsSchema = `
CREATE TABLE IF NOT EXISTS recall_vectors (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id   TEXT NOT NULL,
    message_id   INTEGER NOT NULL,
    chunk_index  INTEGER NOT NULL,
    role         TEXT NOT NULL,
    text         TEXT NOT NULL,
    embedding    BLOB NOT NULL,
    created_at   INTEGER NOT NULL,
    embedder     TEXT NOT NULL,
    UNIQUE (session_id, message_id, chunk_index)
);
CREATE INDEX IF NOT EXISTS recall_vectors_session_idx ON recall_vectors(session_id);
CREATE INDEX IF NOT EXISTS recall_vectors_created_idx ON recall_vectors(created_at);
`

// RecallVectors persists chunk-level embeddings alongside the FTS5
// recall index.
type RecallVectors struct{ db *sql.DB }

// NewRecallVectors returns a RecallVectors sharing the Store's DB.
// Idempotent — applies the schema if absent.
func NewRecallVectors(ctx context.Context, s *Store) (*RecallVectors, error) {
	if _, err := s.db.ExecContext(ctx, recallVectorsSchema); err != nil {
		return nil, fmt.Errorf("sqlite: apply recall_vectors schema: %w", err)
	}
	return &RecallVectors{db: s.db}, nil
}

// VectorRow is the projection returned by [RecallVectors.All] +
// [RecallVectors.ForSession].
type VectorRow struct {
	ID         int64
	SessionID  string
	MessageID  int64
	ChunkIndex int
	Role       string
	Text       string
	Embedding  []byte
	CreatedAt  time.Time
	Embedder   string
}

// Put inserts a chunk row. On duplicate (session, message, chunk)
// the row is replaced — safe for re-embedding.
func (r *RecallVectors) Put(ctx context.Context, row VectorRow) error {
	const q = `
INSERT INTO recall_vectors (session_id, message_id, chunk_index, role, text, embedding, created_at, embedder)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, message_id, chunk_index) DO UPDATE SET
    role = excluded.role,
    text = excluded.text,
    embedding = excluded.embedding,
    created_at = excluded.created_at,
    embedder = excluded.embedder
`
	_, err := r.db.ExecContext(ctx, q,
		row.SessionID, row.MessageID, row.ChunkIndex,
		row.Role, row.Text, row.Embedding,
		row.CreatedAt.UTC().Unix(), row.Embedder)
	if err != nil {
		return fmt.Errorf("sqlite: put recall vector: %w", err)
	}
	return nil
}

// All returns every row. Rows are ordered oldest-first. Callers
// typically iterate via a channel so retrieval doesn't OOM on huge
// stores; this method is intended for tests + admin tools.
func (r *RecallVectors) All(ctx context.Context) ([]VectorRow, error) {
	return r.query(ctx, `SELECT id, session_id, message_id, chunk_index, role, text, embedding, created_at, embedder
FROM recall_vectors ORDER BY created_at ASC`)
}

// Since returns rows newer than after. Same shape as All.
func (r *RecallVectors) Since(ctx context.Context, after time.Time) ([]VectorRow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, session_id, message_id, chunk_index, role, text, embedding, created_at, embedder
FROM recall_vectors WHERE created_at >= ? ORDER BY created_at ASC`, after.UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("sqlite: since recall: %w", err)
	}
	return scanRows(rows)
}

// PurgeOlderThan removes rows whose created_at is before cutoff.
// Returns the number of rows deleted.
func (r *RecallVectors) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM recall_vectors WHERE created_at < ?`, cutoff.UTC().Unix())
	if err != nil {
		return 0, fmt.Errorf("sqlite: purge recall: %w", err)
	}
	return res.RowsAffected()
}

// Count returns the total row count. Cheap; uses the auto-increment
// PK's index.
func (r *RecallVectors) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recall_vectors`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlite: count recall: %w", err)
	}
	return n, nil
}

func (r *RecallVectors) query(ctx context.Context, q string, args ...any) ([]VectorRow, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: recall query: %w", err)
	}
	return scanRows(rows)
}

func scanRows(rows *sql.Rows) ([]VectorRow, error) {
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort close
	var out []VectorRow
	for rows.Next() {
		var v VectorRow
		var ts int64
		if err := rows.Scan(&v.ID, &v.SessionID, &v.MessageID, &v.ChunkIndex, &v.Role, &v.Text, &v.Embedding, &ts, &v.Embedder); err != nil {
			return nil, fmt.Errorf("sqlite: scan recall row: %w", err)
		}
		v.CreatedAt = time.Unix(ts, 0).UTC()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// ErrNoRecall is returned when a lookup finds no rows.
var ErrNoRecall = errors.New("sqlite: no recall rows")
