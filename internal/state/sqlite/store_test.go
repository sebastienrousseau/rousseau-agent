package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
)

func TestStore_SaveLoadRoundtrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	s := agent.NewSession("first")
	s.Append(agent.NewUserText("hello"))

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
		s := agent.NewSession(title)
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

	s := agent.NewSession("t")
	require.NoError(t, store.Save(ctx, s))
	require.NoError(t, store.Delete(ctx, s.ID))

	_, err = store.Load(ctx, s.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
}
