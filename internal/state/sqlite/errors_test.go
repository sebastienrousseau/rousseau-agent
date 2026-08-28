package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen_BadPathErrors(t *testing.T) {
	// A path under a non-existent directory that we can't create because
	// a parent is not a directory.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	require.NoError(t, writeString(blocker, "not a dir"))
	_, err := Open(context.Background(), filepath.Join(blocker, "under-a-file", "db.sqlite"))
	assert.Error(t, err)
}

func TestOpen_InMemoryWorks(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.Close())
}

func TestStore_ListEmpty(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup
	got, err := s.List(context.Background(), 5)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestStore_ListWithoutLimit(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup
	got, err := s.List(context.Background(), 0)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func writeString(path, s string) error {
	return os.WriteFile(path, []byte(s), 0o644)
}
