package oauth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqlitestate "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func openStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := sqlitestate.Open(context.Background(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	inner, err := sqlitestate.NewOAuthTokens(context.Background(), s)
	require.NoError(t, err)
	return NewSQLiteStore(inner)
}

func TestSQLiteStore_PutGetDelete(t *testing.T) {
	s := openStore(t)
	require.NoError(t, s.Put(context.Background(), "google", "alice", []byte("ct")))
	got, ok, err := s.Get(context.Background(), "google", "alice")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []byte("ct"), got)

	require.NoError(t, s.Delete(context.Background(), "google", "alice"))
	_, ok, err = s.Get(context.Background(), "google", "alice")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSQLiteStore_List(t *testing.T) {
	s := openStore(t)
	require.NoError(t, s.Put(context.Background(), "google", "alice", []byte("a")))
	require.NoError(t, s.Put(context.Background(), "github", "bob", []byte("b")))
	rows, err := s.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestSQLiteStore_Iterate(t *testing.T) {
	s := openStore(t)
	require.NoError(t, s.Put(context.Background(), "google", "alice", []byte("a")))
	seen := 0
	require.NoError(t, s.Iterate(context.Background(), func(p, a string, ct []byte) error {
		seen++
		assert.Equal(t, "google", p)
		assert.Equal(t, "alice", a)
		return nil
	}))
	assert.Equal(t, 1, seen)
}
