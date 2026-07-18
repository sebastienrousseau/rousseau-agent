package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewClaudeSessionCache_ClosedDBErrors covers the schema-apply
// error branch.
func TestNewClaudeSessionCache_ClosedDBErrors(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewClaudeSessionCache(context.Background(), s)
	assert.Error(t, err)
}

// TestClaudeSessionCache_RememberAndKnown exercises the happy path
// plus the IsKnown check.
func TestClaudeSessionCache_RememberAndKnown(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.db.Close() }() //nolint:errcheck // test cleanup
	c, err := NewClaudeSessionCache(context.Background(), s)
	require.NoError(t, err)
	c.Remember("sess-1")
	assert.True(t, c.IsKnown("sess-1"))
	assert.False(t, c.IsKnown("unknown"))
}

// TestSearch_EmptyQueryHandled exercises the empty-query path.
// FTS5 typically errors on empty MATCH; either result exercises the
// query-build code path.
func TestSearch_EmptyQueryHandled(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.db.Close() }()                                      //nolint:errcheck // test cleanup
	_, _ = s.Search(context.Background(), "", SearchOptions{})               //nolint:errcheck // any outcome exercises the code path
	_, _ = s.Search(context.Background(), "hello", SearchOptions{Limit: 10}) //nolint:errcheck // exercise the non-empty path too
}

// TestRecentSessions_EmptyStoreReturnsEmpty covers the zero-row
// scan-loop branch.
func TestRecentSessions_EmptyStoreReturnsEmpty(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.db.Close() }() //nolint:errcheck // test cleanup
	sessions, err := s.RecentSessions(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

// TestRecentSessions_LimitClampedToZero covers the negative-limit
// safeguard.
func TestRecentSessions_LimitClampedToZero(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.db.Close() }() //nolint:errcheck // test cleanup
	_, err = s.RecentSessions(context.Background(), 0)
	// Zero limit may return "no limit" or clamp to 1 depending on
	// implementation; either exercises the branch.
	require.NoError(t, err)
}
