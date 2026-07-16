package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openOAuthStore(t *testing.T) *OAuthTokens {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.db.Close() }) //nolint:errcheck // test cleanup
	o, err := NewOAuthTokens(context.Background(), s)
	require.NoError(t, err)
	return o
}

func TestOAuthTokens_PutGet(t *testing.T) {
	o := openOAuthStore(t)
	require.NoError(t, o.Put(context.Background(), "google", "alice", []byte("ct-1")))
	got, ok, err := o.Get(context.Background(), "google", "alice")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []byte("ct-1"), got)
}

func TestOAuthTokens_PutReplaces(t *testing.T) {
	o := openOAuthStore(t)
	require.NoError(t, o.Put(context.Background(), "google", "alice", []byte("v1")))
	require.NoError(t, o.Put(context.Background(), "google", "alice", []byte("v2")))
	got, _, err := o.Get(context.Background(), "google", "alice")
	require.NoError(t, err)
	assert.Equal(t, []byte("v2"), got)
}

func TestOAuthTokens_GetMissing(t *testing.T) {
	o := openOAuthStore(t)
	_, ok, err := o.Get(context.Background(), "google", "nobody")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestOAuthTokens_Delete(t *testing.T) {
	o := openOAuthStore(t)
	require.NoError(t, o.Put(context.Background(), "google", "alice", []byte("v")))
	require.NoError(t, o.Delete(context.Background(), "google", "alice"))
	_, ok, err := o.Get(context.Background(), "google", "alice")
	require.NoError(t, err)
	assert.False(t, ok)
	// Deleting a missing row is not an error.
	require.NoError(t, o.Delete(context.Background(), "google", "nobody"))
}

func TestOAuthTokens_List(t *testing.T) {
	o := openOAuthStore(t)
	require.NoError(t, o.Put(context.Background(), "google", "alice", []byte("a")))
	require.NoError(t, o.Put(context.Background(), "github", "bob", []byte("b")))
	rows, err := o.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestOAuthTokens_Iterate(t *testing.T) {
	o := openOAuthStore(t)
	require.NoError(t, o.Put(context.Background(), "google", "alice", []byte("a")))
	require.NoError(t, o.Put(context.Background(), "github", "bob", []byte("b")))
	seen := 0
	require.NoError(t, o.Iterate(context.Background(), func(p, a string, ct []byte) error {
		seen++
		assert.NotEmpty(t, ct)
		return nil
	}))
	assert.Equal(t, 2, seen)
}
