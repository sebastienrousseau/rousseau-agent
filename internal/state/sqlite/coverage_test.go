package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewJIDMap_ErrorFromClosedStore hits the schema-apply error path
// by handing NewJIDMap a Store whose db has already been closed.
func TestNewJIDMap_ErrorFromClosedStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewJIDMap(context.Background(), s)
	assert.Error(t, err)
}

func TestNewCronStore_ErrorFromClosedStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewCronStore(context.Background(), s)
	assert.Error(t, err)
}

func TestNewOAuthTokens_ErrorFromClosedStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewOAuthTokens(context.Background(), s)
	assert.Error(t, err)
}

func TestNewRecallVectors_ErrorFromClosedStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewRecallVectors(context.Background(), s)
	assert.Error(t, err)
}

// TestJIDMap_GetPutErrorPaths exercises the wrapper error branches
// by driving the store against a closed db.
func TestJIDMap_GetPutErrorPaths(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	m, err := NewJIDMap(context.Background(), s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())

	_, _, err = m.Get(context.Background(), "u")
	assert.Error(t, err)
	assert.Error(t, m.Put(context.Background(), "u", "sess"))
}

func TestOAuth_GetPutDeleteListError(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	o, err := NewOAuthTokens(context.Background(), s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())

	_, _, err = o.Get(context.Background(), "p", "a")
	assert.Error(t, err)
	assert.Error(t, o.Put(context.Background(), "p", "a", []byte("x")))
	assert.Error(t, o.Delete(context.Background(), "p", "a"))
	_, err = o.List(context.Background())
	assert.Error(t, err)
	assert.Error(t, o.Iterate(context.Background(), func(string, string, []byte) error { return nil }))
}

func TestRecallVectors_ErrorsOnClosedDB(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	rv, err := NewRecallVectors(context.Background(), s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())

	assert.Error(t, rv.Put(context.Background(), VectorRow{
		SessionID: "s", MessageID: 1,
		Embedding: []byte{0, 0, 0, 0},
		CreatedAt: time.Now().UTC(),
	}))
	_, err = rv.Count(context.Background())
	assert.Error(t, err)
	_, err = rv.All(context.Background())
	assert.Error(t, err)
	_, err = rv.Since(context.Background(), time.Time{})
	assert.Error(t, err)
	_, err = rv.PurgeOlderThan(context.Background(), time.Now())
	assert.Error(t, err)
}

func TestSearch_ErrorsOnClosedDB(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = s.Search(context.Background(), "hi", SearchOptions{})
	assert.Error(t, err)
	_, err = s.RecentSessions(context.Background(), 5)
	assert.Error(t, err)
	assert.Error(t, s.EnsureSearch(context.Background()))
}
