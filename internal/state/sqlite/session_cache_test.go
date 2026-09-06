package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeSessionCache_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup

	c, err := NewClaudeSessionCache(ctx, s)
	require.NoError(t, err)

	assert.False(t, c.IsKnown("a"))
	c.Remember("a")
	assert.True(t, c.IsKnown("a"))
}

func TestClaudeSessionCache_HotCacheMirrorsDB(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup

	c1, err := NewClaudeSessionCache(ctx, s)
	require.NoError(t, err)
	c1.Remember("shared")

	c2, err := NewClaudeSessionCache(ctx, s)
	require.NoError(t, err)
	assert.True(t, c2.IsKnown("shared"), "second cache should see persisted id")
}

func TestClaudeSessionCache_IdempotentRemember(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup

	c, err := NewClaudeSessionCache(ctx, s)
	require.NoError(t, err)
	c.Remember("x")
	c.Remember("x")
	assert.True(t, c.IsKnown("x"))
}

// TestClaudeSessionCache_ForgetRemovesFromHotAndDB locks in the
// Forget semantics added for the claudecli session-in-use recovery.
// After Forget: (1) the hot cache no longer shows the id, (2) the
// persistent row is gone (verified by opening a second cache
// instance that shares the DB — it must see the deletion).
func TestClaudeSessionCache_ForgetRemovesFromHotAndDB(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup

	c1, err := NewClaudeSessionCache(ctx, s)
	require.NoError(t, err)
	c1.Remember("keep")
	c1.Remember("drop")

	c1.Forget("drop")
	assert.True(t, c1.IsKnown("keep"), "sibling ids must survive Forget")
	assert.False(t, c1.IsKnown("drop"), "Forget must clear the hot cache")

	// Persistence — spin up a fresh cache on the same DB and verify
	// it also sees the deletion (rules out a hot-cache-only Forget).
	c2, err := NewClaudeSessionCache(ctx, s)
	require.NoError(t, err)
	assert.True(t, c2.IsKnown("keep"))
	assert.False(t, c2.IsKnown("drop"), "Forget must delete the persistent row")
}

// TestClaudeSessionCache_ForgetIdempotent verifies Forget on an
// unknown id (never Remember'd, or already Forget'd) is a silent
// no-op. Real-world case: the recovery may fire twice for two
// concurrent turns racing on the same session; the second call
// mustn't error just because the first already cleared the row.
func TestClaudeSessionCache_ForgetIdempotent(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup

	c, err := NewClaudeSessionCache(ctx, s)
	require.NoError(t, err)
	assert.NotPanics(t, func() { c.Forget("never-seen") })
	c.Remember("x")
	c.Forget("x")
	assert.NotPanics(t, func() { c.Forget("x") }) // already forgotten
	assert.False(t, c.IsKnown("x"))
}
