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
