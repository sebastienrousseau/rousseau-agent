package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/identity"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func openIdentityStore(t *testing.T) (*sqlitestore.IdentityStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	base, err := sqlitestore.Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = base.Close() }) //nolint:errcheck // test cleanup
	r, err := sqlitestore.NewIdentityStore(ctx, base)
	require.NoError(t, err)
	return r, ctx
}

func TestResolveUnlinkedReturnsErrNotLinked(t *testing.T) {
	r, ctx := openIdentityStore(t)
	_, err := r.Resolve(ctx, "whatsapp", "+123")
	assert.ErrorIs(t, err, identity.ErrNotLinked)
}

func TestProvisionCreatesIdentityAndHandle(t *testing.T) {
	r, ctx := openIdentityStore(t)
	id, err := r.Provision(ctx, "whatsapp", "+123", "Alice")
	require.NoError(t, err)
	assert.NotEmpty(t, id)

	got, err := r.Resolve(ctx, "whatsapp", "+123")
	require.NoError(t, err)
	assert.Equal(t, id, got)

	rec, err := r.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "Alice", rec.PrimaryDisplay)
	assert.Len(t, rec.Handles, 1)
	assert.Equal(t, "whatsapp", rec.Handles[0].Transport)
	assert.Equal(t, "+123", rec.Handles[0].Sender)
}

func TestProvisionIsIdempotent(t *testing.T) {
	r, ctx := openIdentityStore(t)
	id1, err := r.Provision(ctx, "whatsapp", "+123", "Alice")
	require.NoError(t, err)
	id2, err := r.Provision(ctx, "whatsapp", "+123", "Alice-different")
	require.NoError(t, err)
	assert.Equal(t, id1, id2, "provisioning the same handle twice returns the same ID")

	// Display is NOT updated on re-provision — first-write-wins.
	rec, err := r.Get(ctx, id1)
	require.NoError(t, err)
	assert.Equal(t, "Alice", rec.PrimaryDisplay)
}

func TestLinkAttachesAdditionalHandle(t *testing.T) {
	r, ctx := openIdentityStore(t)
	id, err := r.Provision(ctx, "whatsapp", "+123", "Alice")
	require.NoError(t, err)

	require.NoError(t, r.Link(ctx, id, "slack", "U01234"))
	// Both handles resolve to the same identity.
	got1, err := r.Resolve(ctx, "whatsapp", "+123")
	require.NoError(t, err)
	got2, err := r.Resolve(ctx, "slack", "U01234")
	require.NoError(t, err)
	assert.Equal(t, id, got1)
	assert.Equal(t, id, got2)

	handles, err := r.HandlesFor(ctx, id)
	require.NoError(t, err)
	assert.Len(t, handles, 2)
}

func TestLinkAlreadyLinkedFails(t *testing.T) {
	r, ctx := openIdentityStore(t)
	alice, err := r.Provision(ctx, "whatsapp", "+123", "Alice")
	require.NoError(t, err)
	bob, err := r.Provision(ctx, "whatsapp", "+999", "Bob")
	require.NoError(t, err)

	// Linking Bob's handle to Alice's identity — should fail.
	err = r.Link(ctx, alice, "whatsapp", "+999")
	assert.ErrorIs(t, err, identity.ErrAlreadyLinked)
	_ = bob
}

func TestLinkSameIdentityIsIdempotent(t *testing.T) {
	r, ctx := openIdentityStore(t)
	alice, err := r.Provision(ctx, "whatsapp", "+123", "Alice")
	require.NoError(t, err)
	// Linking a handle that's already bound to the SAME identity
	// must succeed (no-op).
	err = r.Link(ctx, alice, "whatsapp", "+123")
	assert.NoError(t, err)
}

func TestLinkUnknownIdentityFails(t *testing.T) {
	r, ctx := openIdentityStore(t)
	err := r.Link(ctx, "id-does-not-exist", "slack", "U01234")
	assert.ErrorIs(t, err, identity.ErrIdentityNotFound)
}

func TestUnlinkRemovesHandle(t *testing.T) {
	r, ctx := openIdentityStore(t)
	alice, err := r.Provision(ctx, "whatsapp", "+123", "Alice")
	require.NoError(t, err)
	require.NoError(t, r.Link(ctx, alice, "slack", "U01234"))

	require.NoError(t, r.Unlink(ctx, "slack", "U01234"))
	_, err = r.Resolve(ctx, "slack", "U01234")
	assert.ErrorIs(t, err, identity.ErrNotLinked)
	// Alice's whatsapp handle still resolves.
	got, err := r.Resolve(ctx, "whatsapp", "+123")
	require.NoError(t, err)
	assert.Equal(t, alice, got)
}

func TestUnlinkUnknownHandleErrors(t *testing.T) {
	r, ctx := openIdentityStore(t)
	err := r.Unlink(ctx, "whatsapp", "never-linked")
	assert.ErrorIs(t, err, identity.ErrNotLinked)
}

func TestGetUnknownIdentityErrors(t *testing.T) {
	r, ctx := openIdentityStore(t)
	_, err := r.Get(ctx, "id-nowhere")
	assert.ErrorIs(t, err, identity.ErrIdentityNotFound)
}

func TestHandlesForOrderedByVerifiedAt(t *testing.T) {
	r, ctx := openIdentityStore(t)
	alice, err := r.Provision(ctx, "whatsapp", "+123", "Alice")
	require.NoError(t, err)
	require.NoError(t, r.Link(ctx, alice, "slack", "U01234"))
	require.NoError(t, r.Link(ctx, alice, "signal", "signal-1"))

	handles, err := r.HandlesFor(ctx, alice)
	require.NoError(t, err)
	require.Len(t, handles, 3)
	// Should be sorted ascending by verified_at — whatsapp
	// (provisioned first) comes before slack and signal (linked
	// later).
	assert.Equal(t, "whatsapp", handles[0].Transport)
}

func TestNewIdentityStoreRejectsNilStore(t *testing.T) {
	_, err := sqlitestore.NewIdentityStore(context.Background(), nil)
	assert.Error(t, err)
}
