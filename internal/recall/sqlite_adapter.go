package recall

import (
	"context"
	"time"

	sqlitestate "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// SQLiteStore adapts sqlite.RecallVectors to the [Store] interface.
type SQLiteStore struct {
	inner *sqlitestate.RecallVectors
	dims  int
}

// NewSQLiteStore wraps a sqlite.RecallVectors. dims is the expected
// embedding dimensionality; every Put verifies the row's Embedding
// matches, preventing accidental cross-embedder mixing.
func NewSQLiteStore(inner *sqlitestate.RecallVectors, dims int) *SQLiteStore {
	return &SQLiteStore{inner: inner, dims: dims}
}

// Put implements Store.
func (s *SQLiteStore) Put(ctx context.Context, r Row) error {
	if err := ValidateVector(r.Embedding, s.dims); err != nil {
		return err
	}
	return s.inner.Put(ctx, sqlitestate.VectorRow{
		SessionID: r.SessionID, MessageID: r.MessageID,
		ChunkIndex: r.ChunkIndex, Role: r.Role, Text: r.Text,
		Embedding: EncodeVector(r.Embedding),
		CreatedAt: r.CreatedAt, Embedder: r.Embedder,
	})
}

// Since implements Store.
func (s *SQLiteStore) Since(ctx context.Context, after time.Time) ([]Row, error) {
	rows, err := s.inner.Since(ctx, after)
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		vec, err := DecodeVector(r.Embedding, s.dims)
		if err != nil {
			// Skip rows with mismatched dims — they belong to a
			// previous embedder configuration and can't be scored
			// against the current query without re-embedding.
			continue
		}
		out = append(out, Row{
			ID: r.ID, SessionID: r.SessionID, MessageID: r.MessageID,
			ChunkIndex: r.ChunkIndex, Role: r.Role, Text: r.Text,
			Embedding: vec, CreatedAt: r.CreatedAt, Embedder: r.Embedder,
		})
	}
	return out, nil
}

// PurgeOlderThan implements Store.
func (s *SQLiteStore) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.inner.PurgeOlderThan(ctx, cutoff)
}
