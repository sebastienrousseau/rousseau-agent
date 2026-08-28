package oauth

import (
	"context"

	sqlitestate "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// SQLiteStore adapts sqlite.OAuthTokens to the [TokenStore] interface.
// Kept in the oauth package to avoid dragging sqlite imports into
// callers who work with a memory-backed store.
type SQLiteStore struct {
	inner *sqlitestate.OAuthTokens
}

// NewSQLiteStore wraps a sqlite.OAuthTokens.
func NewSQLiteStore(inner *sqlitestate.OAuthTokens) *SQLiteStore {
	return &SQLiteStore{inner: inner}
}

// Put implements TokenStore.
func (s *SQLiteStore) Put(ctx context.Context, provider, accountID string, ct []byte) error {
	return s.inner.Put(ctx, provider, accountID, ct)
}

// Get implements TokenStore.
func (s *SQLiteStore) Get(ctx context.Context, provider, accountID string) ([]byte, bool, error) {
	return s.inner.Get(ctx, provider, accountID)
}

// Delete implements TokenStore.
func (s *SQLiteStore) Delete(ctx context.Context, provider, accountID string) error {
	return s.inner.Delete(ctx, provider, accountID)
}

// List implements TokenStore.
func (s *SQLiteStore) List(ctx context.Context) ([]StoredRow, error) {
	rows, err := s.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]StoredRow, len(rows))
	for i, r := range rows {
		out[i] = StoredRow{Provider: r.Provider, AccountID: r.AccountID}
	}
	return out, nil
}

// Iterate implements TokenStore.
func (s *SQLiteStore) Iterate(ctx context.Context, fn func(provider, accountID string, ct []byte) error) error {
	return s.inner.Iterate(ctx, fn)
}
