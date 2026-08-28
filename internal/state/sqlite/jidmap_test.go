package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJIDMap_PutAndGet(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	jm, err := NewJIDMap(ctx, store)
	require.NoError(t, err)

	require.NoError(t, jm.Put(ctx, "1234@s.whatsapp.net", "sess-1"))

	id, ok, err := jm.Get(ctx, "1234@s.whatsapp.net")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "sess-1", id)
}

func TestJIDMap_GetMissing(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	jm, err := NewJIDMap(ctx, store)
	require.NoError(t, err)

	_, ok, err := jm.Get(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestJIDMap_PutReplaces(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	jm, err := NewJIDMap(ctx, store)
	require.NoError(t, err)

	require.NoError(t, jm.Put(ctx, "x", "a"))
	require.NoError(t, jm.Put(ctx, "x", "b"))

	id, ok, err := jm.Get(ctx, "x")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "b", id)
}
