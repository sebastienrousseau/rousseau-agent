package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgsqlite "github.com/sebastienrousseau/rousseau-agent/pkg/state/sqlite"
)

func TestOpen_InMemoryStore(t *testing.T) {
	ctx := context.Background()
	store, err := pkgsqlite.Open(ctx, ":memory:")
	require.NoError(t, err)
	require.NotNil(t, store)
	assert.NoError(t, store.Close())
}

func TestOpen_InvalidPathErrors(t *testing.T) {
	// A path in a non-existent directory should fail to open.
	_, err := pkgsqlite.Open(context.Background(), "/nonexistent/directory/does-not-exist.db")
	assert.Error(t, err)
}

func TestNewJIDMap(t *testing.T) {
	ctx := context.Background()
	store, err := pkgsqlite.Open(ctx, ":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup

	jm, err := pkgsqlite.NewJIDMap(ctx, store)
	require.NoError(t, err)
	assert.NotNil(t, jm)
}

func TestNewCronStore(t *testing.T) {
	ctx := context.Background()
	store, err := pkgsqlite.Open(ctx, ":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup

	cs, err := pkgsqlite.NewCronStore(ctx, store)
	require.NoError(t, err)
	assert.NotNil(t, cs)
}
