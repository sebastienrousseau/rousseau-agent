package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openSessionCacheTest opens a ClaudeSessionCache against the
// CI/dev Postgres and truncates the claude_sessions table so each
// test starts from a clean slate. Guarded on
// ROUSSEAU_TEST_POSTGRES_URL like sessions + cron + jidmap.
func openSessionCacheTest(t *testing.T) (*Store, *ClaudeSessionCache) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	c, err := NewClaudeSessionCache(ctx, store)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `TRUNCATE TABLE claude_sessions`)
	require.NoError(t, err)
	return store, c
}

// -- unit-only (no DB) --

func TestNewClaudeSessionCache_SchemaIdempotent(t *testing.T) {
	// Compile-check the const we ship — an accidental rename
	// would cause the daemon to fail schema-apply on upgrade.
	assert.Contains(t, claudeSessionsSchema, "CREATE TABLE IF NOT EXISTS claude_sessions")
	assert.Contains(t, claudeSessionsSchema, "TIMESTAMPTZ NOT NULL DEFAULT NOW()")
}

// -- integration --

func TestIntegration_ClaudeSessionCache_RoundTrip(t *testing.T) {
	_, c := openSessionCacheTest(t)

	assert.False(t, c.IsKnown("a"))
	c.Remember("a")
	assert.True(t, c.IsKnown("a"))
}

func TestIntegration_ClaudeSessionCache_HotCacheMirrorsDB(t *testing.T) {
	// Second cache over the same DB must see previously-persisted
	// IDs even though its in-memory hot map is empty. Proves the DB
	// path — not just the hot cache — is the source of truth.
	ctx := context.Background()
	store, c1 := openSessionCacheTest(t)
	c1.Remember("shared")

	c2, err := NewClaudeSessionCache(ctx, store)
	require.NoError(t, err)
	assert.True(t, c2.IsKnown("shared"), "second cache should see persisted id")
}

func TestIntegration_ClaudeSessionCache_IdempotentRemember(t *testing.T) {
	// ON CONFLICT DO NOTHING — Claude Code may re-emit the same
	// session ID and Remember must silently no-op.
	_, c := openSessionCacheTest(t)
	c.Remember("x")
	c.Remember("x")
	assert.True(t, c.IsKnown("x"))
}
