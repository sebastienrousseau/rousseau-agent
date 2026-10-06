package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
)

func TestStore_SaveLoadRoundtrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	s := model.NewSession("first")
	s.Append(model.NewUserText("hello"))

	require.NoError(t, store.Save(ctx, s))

	got, err := store.Load(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, s.ID, got.ID)
	assert.Equal(t, "first", got.Title)
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "hello", got.Messages[0].Content[0].Text)
}

func TestStore_LoadMissing(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	_, err = store.Load(ctx, "nope")
	assert.ErrorIs(t, err, state.ErrNotFound)
}

func TestStore_List(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	for _, title := range []string{"a", "b", "c"} {
		s := model.NewSession(title)
		require.NoError(t, store.Save(ctx, s))
	}
	summaries, err := store.List(ctx, 0)
	require.NoError(t, err)
	assert.Len(t, summaries, 3)
}

func TestStore_Delete(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	s := model.NewSession("t")
	require.NoError(t, store.Save(ctx, s))
	require.NoError(t, store.Delete(ctx, s.ID))

	_, err = store.Load(ctx, s.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
}

// TestOpen_PragmasApplyToEveryPooledConnection pins that busy_timeout
// and foreign_keys are set per connection (via the DSN), not only on
// whichever pooled connection happened to run the PRAGMA statement.
func TestOpen_PragmasApplyToEveryPooledConnection(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup

	// Hold two connections at once so the pool must hand out a
	// second, fresh one.
	c1, err := s.db.Conn(ctx)
	require.NoError(t, err)
	defer c1.Close() //nolint:errcheck // test cleanup
	c2, err := s.db.Conn(ctx)
	require.NoError(t, err)
	defer c2.Close() //nolint:errcheck // test cleanup

	for i, c := range []*sql.Conn{c1, c2} {
		var busy, fk int
		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy))
		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk))
		assert.Equal(t, 15000, busy, "conn %d busy_timeout", i)
		assert.Equal(t, 1, fk, "conn %d foreign_keys", i)
	}
}

func TestFileDSN(t *testing.T) {
	assert.Equal(t, ":memory:", fileDSN(":memory:"))
	assert.Equal(t, "file:x.db?mode=ro", fileDSN("file:x.db?mode=ro"))
	assert.Equal(t, "/tmp/odd?name.db", fileDSN("/tmp/odd?name.db"))
	got := fileDSN("/var/lib/rousseau/sessions.db")
	assert.True(t, strings.HasPrefix(got, "file:/var/lib/rousseau/sessions.db?"), got)
	assert.Contains(t, got, "busy_timeout(15000)")
}
