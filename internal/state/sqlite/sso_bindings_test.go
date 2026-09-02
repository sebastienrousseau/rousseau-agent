package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

func openSSOBindings(t *testing.T) *SSOBindings {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup
	b, err := NewSSOBindings(ctx, store)
	require.NoError(t, err)
	return b
}

func TestSSOBindings_BindThenLookupReturnsIdentity(t *testing.T) {
	b := openSSOBindings(t)
	ctx := context.Background()

	id := sso.Identity{
		Subject:     "okta|abc123",
		Email:       "alice@example.com",
		DisplayName: "Alice Example",
		Groups:      []string{"eng"},
	}
	require.NoError(t, b.Bind(ctx, "whatsapp", "+14155551212", id, time.Now().Add(time.Hour)))

	got, ok, err := b.Lookup(ctx, "whatsapp", "+14155551212")
	require.NoError(t, err)
	require.True(t, ok, "just-bound identity must be found")
	assert.Equal(t, "okta|abc123", got.Subject)
	assert.Equal(t, "alice@example.com", got.Email)
	assert.Equal(t, "Alice Example", got.DisplayName)
	assert.ElementsMatch(t, []string{"eng"}, got.Groups)
}

func TestSSOBindings_LookupMissingReturnsFalse(t *testing.T) {
	b := openSSOBindings(t)
	_, ok, err := b.Lookup(context.Background(), "whatsapp", "+missing")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSSOBindings_ExpiredBindingIsHidden(t *testing.T) {
	// Load-bearing property: an expired binding MUST NOT surface
	// through Lookup. Callers rely on this to avoid an exp
	// double-check.
	b := openSSOBindings(t)
	ctx := context.Background()

	id := sso.Identity{Subject: "okta|expired"}
	require.NoError(t, b.Bind(ctx, "whatsapp", "+expiring", id, time.Now().Add(-1*time.Minute)))

	_, ok, err := b.Lookup(ctx, "whatsapp", "+expiring")
	require.NoError(t, err)
	assert.False(t, ok, "expired binding must be filtered")
}

func TestSSOBindings_RebindExtendsExpiry(t *testing.T) {
	// Operator scenario: user re-runs /login before their old
	// binding expires. New expiry must win — no "already bound"
	// error.
	b := openSSOBindings(t)
	ctx := context.Background()

	id := sso.Identity{Subject: "okta|renew"}
	// Bind with a stale expiry.
	require.NoError(t, b.Bind(ctx, "whatsapp", "+renew", id, time.Now().Add(-1*time.Minute)))
	// Confirm the stale one filters out.
	_, ok, err := b.Lookup(ctx, "whatsapp", "+renew")
	require.NoError(t, err)
	require.False(t, ok)
	// Re-bind fresh.
	require.NoError(t, b.Bind(ctx, "whatsapp", "+renew", id, time.Now().Add(time.Hour)))
	got, ok, err := b.Lookup(ctx, "whatsapp", "+renew")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "okta|renew", got.Subject)
}

func TestSSOBindings_UnbindRemoves(t *testing.T) {
	b := openSSOBindings(t)
	ctx := context.Background()

	id := sso.Identity{Subject: "okta|out"}
	require.NoError(t, b.Bind(ctx, "whatsapp", "+out", id, time.Now().Add(time.Hour)))
	require.NoError(t, b.Unbind(ctx, "whatsapp", "+out"))
	_, ok, err := b.Lookup(ctx, "whatsapp", "+out")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSSOBindings_UnbindMissingIsNoop(t *testing.T) {
	b := openSSOBindings(t)
	// Unbind is documented as idempotent — removing a
	// non-existent binding must not error.
	assert.NoError(t, b.Unbind(context.Background(), "whatsapp", "+never-bound"))
}

func TestSSOBindings_CountExcludesExpired(t *testing.T) {
	b := openSSOBindings(t)
	ctx := context.Background()

	require.NoError(t, b.Bind(ctx, "whatsapp", "+a", sso.Identity{Subject: "a"}, time.Now().Add(time.Hour)))
	require.NoError(t, b.Bind(ctx, "whatsapp", "+b", sso.Identity{Subject: "b"}, time.Now().Add(time.Hour)))
	require.NoError(t, b.Bind(ctx, "whatsapp", "+c", sso.Identity{Subject: "c"}, time.Now().Add(-1*time.Minute)))

	n, err := b.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "expired binding must not be counted")
}

func TestSSOBindings_DifferentTransportsAreSeparate(t *testing.T) {
	// Same external ID under different transports must not
	// collide. Property: composite primary key correctness.
	b := openSSOBindings(t)
	ctx := context.Background()

	require.NoError(t, b.Bind(ctx, "whatsapp", "shared", sso.Identity{Subject: "wa"}, time.Now().Add(time.Hour)))
	require.NoError(t, b.Bind(ctx, "slack", "shared", sso.Identity{Subject: "sl"}, time.Now().Add(time.Hour)))

	wa, ok, err := b.Lookup(ctx, "whatsapp", "shared")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "wa", wa.Subject)

	sl, ok, err := b.Lookup(ctx, "slack", "shared")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "sl", sl.Subject)
}

func TestNoBindings_AlwaysMissAndZero(t *testing.T) {
	// The fail-safe implementation is inert. Wired into the
	// router when the operator hasn't configured SSO.
	n := sso.NoBindings{}
	ctx := context.Background()
	assert.NoError(t, n.Bind(ctx, "whatsapp", "x", sso.Identity{Subject: "s"}, time.Now().Add(time.Hour)))
	_, ok, err := n.Lookup(ctx, "whatsapp", "x")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.NoError(t, n.Unbind(ctx, "whatsapp", "x"))
	c, err := n.Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, c)
}
