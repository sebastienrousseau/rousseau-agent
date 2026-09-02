package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openOAuthTest opens an OAuthTokens against the CI/dev Postgres
// and truncates the oauth_tokens table so each test starts from a
// clean slate. Guarded on ROUSSEAU_TEST_POSTGRES_URL like the
// other integration tests.
func openOAuthTest(t *testing.T) *OAuthTokens {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	o, err := NewOAuthTokens(ctx, store)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `TRUNCATE TABLE oauth_tokens`)
	require.NoError(t, err)
	return o
}

// -- unit-only (no DB) --

func TestNewOAuthTokens_SchemaIdempotent(t *testing.T) {
	// Compile-check the const we ship — an accidental rename
	// would cause the daemon to fail schema-apply on upgrade.
	assert.Contains(t, oauthTokensSchema, "CREATE TABLE IF NOT EXISTS oauth_tokens")
	assert.Contains(t, oauthTokensSchema, "ciphertext BYTEA NOT NULL")
	assert.Contains(t, oauthTokensSchema, "TIMESTAMPTZ NOT NULL DEFAULT NOW()")
	assert.Contains(t, oauthTokensSchema, "PRIMARY KEY (provider, account_id)")
}

// -- integration --

func TestIntegration_OAuthTokens_PutGet(t *testing.T) {
	o := openOAuthTest(t)
	ctx := context.Background()
	require.NoError(t, o.Put(ctx, "google", "alice", []byte("ct-1")))
	got, ok, err := o.Get(ctx, "google", "alice")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []byte("ct-1"), got)
}

func TestIntegration_OAuthTokens_PutReplaces(t *testing.T) {
	// Refresh-token / re-seal path — same (provider, account) must
	// overwrite in place, not accumulate rows.
	o := openOAuthTest(t)
	ctx := context.Background()
	require.NoError(t, o.Put(ctx, "google", "alice", []byte("v1")))
	require.NoError(t, o.Put(ctx, "google", "alice", []byte("v2")))
	got, _, err := o.Get(ctx, "google", "alice")
	require.NoError(t, err)
	assert.Equal(t, []byte("v2"), got)
}

func TestIntegration_OAuthTokens_GetMissing(t *testing.T) {
	o := openOAuthTest(t)
	_, ok, err := o.Get(context.Background(), "google", "nobody")
	require.NoError(t, err)
	assert.False(t, ok, "unmapped account must return ok=false, not ErrNoRows leak")
}

func TestIntegration_OAuthTokens_Delete(t *testing.T) {
	o := openOAuthTest(t)
	ctx := context.Background()
	require.NoError(t, o.Put(ctx, "google", "alice", []byte("v")))
	require.NoError(t, o.Delete(ctx, "google", "alice"))
	_, ok, err := o.Get(ctx, "google", "alice")
	require.NoError(t, err)
	assert.False(t, ok)
	// Deleting a missing row must not error — matches SQLite driver
	// so idempotent cleanup scripts do not need per-driver branches.
	require.NoError(t, o.Delete(ctx, "google", "nobody"))
}

func TestIntegration_OAuthTokens_List(t *testing.T) {
	o := openOAuthTest(t)
	ctx := context.Background()
	require.NoError(t, o.Put(ctx, "google", "alice", []byte("a")))
	require.NoError(t, o.Put(ctx, "github", "bob", []byte("b")))
	rows, err := o.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	// ORDER BY provider, account_id — github before google.
	assert.Equal(t, "github", rows[0].Provider)
	assert.Equal(t, "google", rows[1].Provider)
	// updated_at must be present + UTC-normalised.
	assert.False(t, rows[0].UpdatedAt.IsZero(), "updated_at must be stamped by DEFAULT NOW()")
	assert.Equal(t, "UTC", rows[0].UpdatedAt.Location().String())
}

func TestIntegration_OAuthTokens_Iterate(t *testing.T) {
	// Iterate is the rotate-key admin path — must yield the raw
	// ciphertext blob unchanged so callers can re-seal it.
	o := openOAuthTest(t)
	ctx := context.Background()
	require.NoError(t, o.Put(ctx, "google", "alice", []byte("a")))
	require.NoError(t, o.Put(ctx, "github", "bob", []byte("b")))
	seen := 0
	require.NoError(t, o.Iterate(ctx, func(_, _ string, ct []byte) error {
		seen++
		assert.NotEmpty(t, ct)
		return nil
	}))
	assert.Equal(t, 2, seen)
}
