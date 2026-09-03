package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/identity"
)

// openIdentityTest opens an IdentityStore on the CI/dev Postgres,
// applies the schema, and truncates both tables so each test
// starts clean. Guarded on ROUSSEAU_TEST_POSTGRES_URL like the
// other integration tests in this package.
//
// TRUNCATE identities CASCADE cascades to identity_handles thanks
// to the FK, so we only issue one TRUNCATE — same reason a
// caller doesn't have to remember to clean both.
func openIdentityTest(t *testing.T) (*IdentityStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	r, err := NewIdentityStore(ctx, store)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `TRUNCATE TABLE identities CASCADE`)
	require.NoError(t, err)
	return r, ctx
}

// -- unit-only (no DB) --

func TestNewIdentityStore_SchemaIdempotent(t *testing.T) {
	// Compile-check the const we ship — an accidental rename or
	// missing FK would break the daemon at boot.
	assert.Contains(t, identitySchema, "CREATE TABLE IF NOT EXISTS identities")
	assert.Contains(t, identitySchema, "CREATE TABLE IF NOT EXISTS identity_handles")
	assert.Contains(t, identitySchema, "REFERENCES identities(id) ON DELETE CASCADE")
	assert.Contains(t, identitySchema, "PRIMARY KEY (transport, sender)")
	assert.Contains(t, identitySchema, "idx_identity_handles_identity")
	assert.Contains(t, identitySchema, "TIMESTAMPTZ NOT NULL DEFAULT NOW()")
}

func TestNewIdentityStore_RejectsNilStore(t *testing.T) {
	// Matches the SQLite driver's nil-check so downstream callers
	// don't need per-driver branches.
	_, err := NewIdentityStore(context.Background(), nil)
	assert.Error(t, err)
}

// -- integration --

func TestIntegration_IdentityProvisionIsIdempotent(t *testing.T) {
	// Provision must return the SAME id when called twice with the
	// same (transport, sender). Any drift here would break the
	// auto-provisioning path — the router calls Provision on
	// every unrecognised inbound.
	r, ctx := openIdentityTest(t)

	first, err := r.Provision(ctx, "whatsapp", "+123", "alice")
	require.NoError(t, err)
	second, err := r.Provision(ctx, "whatsapp", "+123", "alice")
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

func TestIntegration_IdentityResolveReturnsErrNotLinkedForMissing(t *testing.T) {
	// Load-bearing: the router distinguishes "unknown sender"
	// (ErrNotLinked → auto-provision) from other errors
	// (surface). Pin the sentinel.
	r, ctx := openIdentityTest(t)
	_, err := r.Resolve(ctx, "whatsapp", "never-bound")
	require.ErrorIs(t, err, identity.ErrNotLinked)
}

func TestIntegration_IdentityLinkAllowsSecondHandle(t *testing.T) {
	// Cross-transport identity — the whole point. Provision on
	// whatsapp, then Link a slack handle, and both Resolve to
	// the same identity.
	r, ctx := openIdentityTest(t)

	id, err := r.Provision(ctx, "whatsapp", "+123", "alice")
	require.NoError(t, err)

	require.NoError(t, r.Link(ctx, id, "slack", "U01234"))

	slackID, err := r.Resolve(ctx, "slack", "U01234")
	require.NoError(t, err)
	assert.Equal(t, id, slackID)

	got, err := r.Get(ctx, id)
	require.NoError(t, err)
	assert.Len(t, got.Handles, 2)
}

func TestIntegration_IdentityLinkIdempotent(t *testing.T) {
	// Re-linking the same handle to the same identity is a no-op,
	// not an error. Matches SQLite so operator retry scripts
	// don't need per-driver branches.
	r, ctx := openIdentityTest(t)
	id, err := r.Provision(ctx, "whatsapp", "+123", "")
	require.NoError(t, err)
	require.NoError(t, r.Link(ctx, id, "slack", "U01234"))
	require.NoError(t, r.Link(ctx, id, "slack", "U01234"))
}

func TestIntegration_IdentityLinkRejectsAlreadyLinkedElsewhere(t *testing.T) {
	// A handle already linked to identity A must not silently
	// re-bind to identity B — otherwise an attacker who provisions
	// on a new transport could hijack the primary identity.
	r, ctx := openIdentityTest(t)
	idA, err := r.Provision(ctx, "whatsapp", "+123", "alice")
	require.NoError(t, err)
	idB, err := r.Provision(ctx, "signal", "+999", "bob")
	require.NoError(t, err)

	err = r.Link(ctx, idB, "whatsapp", "+123")
	require.ErrorIs(t, err, identity.ErrAlreadyLinked)

	// idA still owns the whatsapp handle.
	resolved, err := r.Resolve(ctx, "whatsapp", "+123")
	require.NoError(t, err)
	assert.Equal(t, idA, resolved)
}

func TestIntegration_IdentityUnlinkRemovesHandle(t *testing.T) {
	r, ctx := openIdentityTest(t)
	id, err := r.Provision(ctx, "whatsapp", "+123", "alice")
	require.NoError(t, err)
	require.NoError(t, r.Link(ctx, id, "slack", "U01234"))
	require.NoError(t, r.Unlink(ctx, "slack", "U01234"))

	_, err = r.Resolve(ctx, "slack", "U01234")
	require.ErrorIs(t, err, identity.ErrNotLinked)
	// The primary handle survives.
	primary, err := r.Resolve(ctx, "whatsapp", "+123")
	require.NoError(t, err)
	assert.Equal(t, id, primary)
}

func TestIntegration_IdentityUnlinkMissingReturnsErrNotLinked(t *testing.T) {
	// Symmetric error surface to Resolve — callers can rely on
	// the ErrNotLinked sentinel across both drivers.
	r, ctx := openIdentityTest(t)
	err := r.Unlink(ctx, "whatsapp", "never-there")
	require.ErrorIs(t, err, identity.ErrNotLinked)
}

func TestIntegration_IdentityGetUnknownReturnsErrIdentityNotFound(t *testing.T) {
	r, ctx := openIdentityTest(t)
	_, err := r.Get(ctx, identity.ID("id-nope"))
	require.ErrorIs(t, err, identity.ErrIdentityNotFound)
}

func TestIntegration_IdentityHandlesForOrderedByVerifiedAsc(t *testing.T) {
	// /whoami renders handles in verified-at order so the primary
	// (auto-provisioned) transport shows first. Pin so a Postgres
	// upgrade with different default sort stability doesn't cause
	// a visible UI shift.
	r, ctx := openIdentityTest(t)
	id, err := r.Provision(ctx, "whatsapp", "+123", "alice")
	require.NoError(t, err)
	require.NoError(t, r.Link(ctx, id, "slack", "U01234"))
	require.NoError(t, r.Link(ctx, id, "discord", "d-999"))

	handles, err := r.HandlesFor(ctx, id)
	require.NoError(t, err)
	require.Len(t, handles, 3)
	assert.Equal(t, "whatsapp", handles[0].Transport)
	assert.Equal(t, "slack", handles[1].Transport)
	assert.Equal(t, "discord", handles[2].Transport)
}

func TestIntegration_IdentityDeleteCascadesToHandles(t *testing.T) {
	// Postgres-specific: ON DELETE CASCADE on the FK means
	// removing an identity row wipes its handles. SQLite relies
	// on the operator's connection having `foreign_keys = ON`;
	// on Postgres this is unconditional so worth pinning
	// separately (proves the FK is really there).
	r, ctx := openIdentityTest(t)
	id, err := r.Provision(ctx, "whatsapp", "+123", "alice")
	require.NoError(t, err)

	// Direct DELETE against the underlying DB — the driver
	// doesn't expose an identity-delete method today.
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup
	_, err = store.db.ExecContext(ctx, `DELETE FROM identities WHERE id=$1`, string(id))
	require.NoError(t, err)

	_, err = r.Resolve(ctx, "whatsapp", "+123")
	require.ErrorIs(t, err, identity.ErrNotLinked, "handle must be cascaded away with the identity")
}
