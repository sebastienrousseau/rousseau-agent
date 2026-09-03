package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// openSSOBindingsTest opens an SSOBindings on the CI/dev Postgres,
// applies the schema, and truncates so each test starts clean.
// Guarded on ROUSSEAU_TEST_POSTGRES_URL like the other integration
// tests in this package.
func openSSOBindingsTest(t *testing.T) (*SSOBindings, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	b, err := NewSSOBindings(ctx, store)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `TRUNCATE TABLE sso_bindings`)
	require.NoError(t, err)
	return b, ctx
}

// -- unit-only (no DB) --

func TestNewSSOBindings_SchemaIdempotent(t *testing.T) {
	// Compile-check the const we ship — an accidental rename
	// would cause the daemon to fail schema-apply on upgrade.
	assert.Contains(t, ssoBindingsSchema, "CREATE TABLE IF NOT EXISTS sso_bindings")
	assert.Contains(t, ssoBindingsSchema, "TIMESTAMPTZ NOT NULL DEFAULT NOW()")
	assert.Contains(t, ssoBindingsSchema, "identity    JSONB NOT NULL")
	assert.Contains(t, ssoBindingsSchema, "PRIMARY KEY (transport, external_id)")
	assert.Contains(t, ssoBindingsSchema, "idx_sso_bindings_expires_at")
}

// -- integration --

func TestIntegration_SSOBindings_BindLookupRoundtrip(t *testing.T) {
	b, ctx := openSSOBindingsTest(t)
	id := sso.Identity{Subject: "alice@example.com", DisplayName: "Alice", Groups: []string{"eng", "sre"}}
	require.NoError(t, b.Bind(ctx, "whatsapp", "447900123456", id, time.Now().Add(time.Hour)))

	got, ok, err := b.Lookup(ctx, "whatsapp", "447900123456")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, id.Subject, got.Subject)
	assert.Equal(t, id.DisplayName, got.DisplayName)
	assert.Equal(t, id.Groups, got.Groups)
}

func TestIntegration_SSOBindings_LookupExpiredReturnsFalse(t *testing.T) {
	// Load-bearing: the daemon does not run a background sweeper,
	// so Lookup is the sole guard against handing a caller a
	// stale binding. Pin that behaviour.
	b, ctx := openSSOBindingsTest(t)
	id := sso.Identity{Subject: "expired@example.com"}
	require.NoError(t, b.Bind(ctx, "whatsapp", "expired", id, time.Now().Add(-time.Second)))

	_, ok, err := b.Lookup(ctx, "whatsapp", "expired")
	require.NoError(t, err)
	assert.False(t, ok, "expired row must not surface via Lookup")
}

func TestIntegration_SSOBindings_LookupMissingReturnsFalse(t *testing.T) {
	// Missing row returns ok=false + nil error, NOT ErrNoRows —
	// matches the SQLite driver so callers don't need per-driver
	// branches for the "unknown sender" case.
	b, ctx := openSSOBindingsTest(t)
	_, ok, err := b.Lookup(ctx, "whatsapp", "never-bound")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestIntegration_SSOBindings_RebindExtendsExpiry(t *testing.T) {
	// Operator-visible contract: /login-ing again refreshes the
	// binding rather than erroring "already bound". Pinned so a
	// future refactor doesn't accidentally strip the ON CONFLICT
	// DO UPDATE clause.
	b, ctx := openSSOBindingsTest(t)
	id := sso.Identity{Subject: "alice"}
	require.NoError(t, b.Bind(ctx, "whatsapp", "alice-jid", id, time.Now().Add(-time.Second)))

	// Second bind extends past now.
	require.NoError(t, b.Bind(ctx, "whatsapp", "alice-jid", id, time.Now().Add(time.Hour)))

	_, ok, err := b.Lookup(ctx, "whatsapp", "alice-jid")
	require.NoError(t, err)
	assert.True(t, ok, "re-bind must extend expiry, not error")
}

func TestIntegration_SSOBindings_UnbindIdempotent(t *testing.T) {
	b, ctx := openSSOBindingsTest(t)
	require.NoError(t, b.Bind(ctx, "whatsapp", "alice", sso.Identity{Subject: "alice"}, time.Now().Add(time.Hour)))
	require.NoError(t, b.Unbind(ctx, "whatsapp", "alice"))
	// Second unbind is a no-op — SCIM §3.6-style safe retry.
	require.NoError(t, b.Unbind(ctx, "whatsapp", "alice"))

	_, ok, err := b.Lookup(ctx, "whatsapp", "alice")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestIntegration_SSOBindings_CountExcludesExpired(t *testing.T) {
	b, ctx := openSSOBindingsTest(t)
	require.NoError(t, b.Bind(ctx, "whatsapp", "live1", sso.Identity{Subject: "a"}, time.Now().Add(time.Hour)))
	require.NoError(t, b.Bind(ctx, "whatsapp", "live2", sso.Identity{Subject: "b"}, time.Now().Add(time.Hour)))
	require.NoError(t, b.Bind(ctx, "slack", "stale", sso.Identity{Subject: "c"}, time.Now().Add(-time.Second)))

	n, err := b.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "Count reports valid bindings only")
}

func TestIntegration_SSOBindings_PreservesTransportScope(t *testing.T) {
	// The same external_id across two transports must not collide
	// — composite PK is (transport, external_id), so both bindings
	// coexist and Lookup returns the transport-specific identity.
	b, ctx := openSSOBindingsTest(t)
	require.NoError(t, b.Bind(ctx, "whatsapp", "u1", sso.Identity{Subject: "wa"}, time.Now().Add(time.Hour)))
	require.NoError(t, b.Bind(ctx, "slack", "u1", sso.Identity{Subject: "sl"}, time.Now().Add(time.Hour)))

	got, ok, err := b.Lookup(ctx, "whatsapp", "u1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "wa", got.Subject)

	got, ok, err = b.Lookup(ctx, "slack", "u1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "sl", got.Subject)
}
