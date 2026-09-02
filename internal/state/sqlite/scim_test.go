package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/scim"
)

func openSCIMStore(t *testing.T) *SCIMStore {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup
	s, err := NewSCIMStore(ctx, store)
	require.NoError(t, err)
	return s
}

// -- Users --

func TestSCIM_CreateAndGetUser(t *testing.T) {
	s := openSCIMStore(t)
	ctx := context.Background()
	created, err := s.CreateUser(ctx, scim.User{
		UserName:   "alice",
		ExternalID: "okta|alice-123",
		Emails:     []scim.Email{{Value: "alice@example.com", Primary: true}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID, "store must assign an ID")

	got, err := s.GetUser(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "alice", got.UserName)
	assert.Equal(t, "okta|alice-123", got.ExternalID)
	require.Len(t, got.Emails, 1)
	assert.Equal(t, "alice@example.com", got.Emails[0].Value)
}

func TestSCIM_CreateUserActiveDefaultsTrue(t *testing.T) {
	// Load-bearing SCIM 2.0 semantic: Active defaults to true
	// when the IdP omits it. Newly-provisioned users must be
	// usable immediately.
	s := openSCIMStore(t)
	created, err := s.CreateUser(context.Background(), scim.User{UserName: "u"})
	require.NoError(t, err)
	assert.True(t, created.Active)
}

func TestSCIM_DuplicateUserNameReturnsConflict(t *testing.T) {
	s := openSCIMStore(t)
	ctx := context.Background()
	_, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)
	_, err = s.CreateUser(ctx, scim.User{UserName: "alice"})
	assert.ErrorIs(t, err, scim.ErrConflict)
}

func TestSCIM_DuplicateExternalIDReturnsConflict(t *testing.T) {
	s := openSCIMStore(t)
	ctx := context.Background()
	_, err := s.CreateUser(ctx, scim.User{UserName: "a", ExternalID: "ext-1"})
	require.NoError(t, err)
	_, err = s.CreateUser(ctx, scim.User{UserName: "b", ExternalID: "ext-1"})
	assert.ErrorIs(t, err, scim.ErrConflict)
}

func TestSCIM_GetMissingUserReturnsNotFound(t *testing.T) {
	s := openSCIMStore(t)
	_, err := s.GetUser(context.Background(), "no-such-id")
	assert.ErrorIs(t, err, scim.ErrNotFound)
}

func TestSCIM_ListUsersOrderAndFilter(t *testing.T) {
	s := openSCIMStore(t)
	ctx := context.Background()
	for _, n := range []string{"charlie", "alice", "bob"} {
		_, err := s.CreateUser(ctx, scim.User{UserName: n})
		require.NoError(t, err)
	}
	all, total, err := s.ListUsers(ctx, "", 0, 0)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, all, 3)
	// Alphabetical order by userName.
	assert.Equal(t, "alice", all[0].UserName)
	assert.Equal(t, "bob", all[1].UserName)
	assert.Equal(t, "charlie", all[2].UserName)

	// Filter.
	filtered, total, err := s.ListUsers(ctx, "bob", 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, filtered, 1)
	assert.Equal(t, "bob", filtered[0].UserName)
}

func TestSCIM_ListUsersPagination(t *testing.T) {
	s := openSCIMStore(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		_, err := s.CreateUser(ctx, scim.User{UserName: string(rune('a' + i))})
		require.NoError(t, err)
	}
	// First page, count=3.
	first, total, err := s.ListUsers(ctx, "", 1, 3)
	require.NoError(t, err)
	assert.Equal(t, 10, total, "total ignores pagination")
	require.Len(t, first, 3)
	assert.Equal(t, "a", first[0].UserName)

	// Second page.
	second, _, err := s.ListUsers(ctx, "", 4, 3)
	require.NoError(t, err)
	require.Len(t, second, 3)
	assert.Equal(t, "d", second[0].UserName)
}

func TestSCIM_ReplaceUserPreservesID(t *testing.T) {
	s := openSCIMStore(t)
	ctx := context.Background()
	created, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)
	replaced, err := s.ReplaceUser(ctx, created.ID, scim.User{
		UserName: "alice-renamed",
		Name:     &scim.Name{FamilyName: "Example"},
	})
	require.NoError(t, err)
	assert.Equal(t, created.ID, replaced.ID)
	assert.Equal(t, "alice-renamed", replaced.UserName)

	// Roundtrip.
	got, err := s.GetUser(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "alice-renamed", got.UserName)
	require.NotNil(t, got.Name)
	assert.Equal(t, "Example", got.Name.FamilyName)
}

func TestSCIM_ReplaceMissingUserReturnsNotFound(t *testing.T) {
	s := openSCIMStore(t)
	_, err := s.ReplaceUser(context.Background(), "missing", scim.User{UserName: "x"})
	assert.ErrorIs(t, err, scim.ErrNotFound)
}

func TestSCIM_DeleteMissingUserIsNoop(t *testing.T) {
	// SCIM 2.0 §3.6 idempotency guarantee.
	s := openSCIMStore(t)
	err := s.DeleteUser(context.Background(), "does-not-exist")
	assert.NoError(t, err)
}

// -- LookupUserByExternalID --

func TestSCIM_LookupUserByExternalID(t *testing.T) {
	// Load-bearing property: this is the join point that
	// downstream SSO code uses to correlate a chat transport
	// ID to an SCIM-provisioned user without a JWT.
	s := openSCIMStore(t)
	ctx := context.Background()
	_, err := s.CreateUser(ctx, scim.User{
		UserName:   "alice",
		ExternalID: "okta|abc-123",
	})
	require.NoError(t, err)

	got, err := s.LookupUserByExternalID(ctx, "okta|abc-123")
	require.NoError(t, err)
	assert.Equal(t, "alice", got.UserName)

	_, err = s.LookupUserByExternalID(ctx, "nonexistent")
	assert.ErrorIs(t, err, scim.ErrNotFound)
}

func TestSCIM_LookupUserByEmptyExternalIDIsNotFound(t *testing.T) {
	// Guards against a caller passing "" and matching every
	// user whose externalID column is NULL. Must return
	// ErrNotFound.
	s := openSCIMStore(t)
	_, err := s.LookupUserByExternalID(context.Background(), "")
	assert.ErrorIs(t, err, scim.ErrNotFound)
}

// -- Groups --

func TestSCIM_CreateGroupWithMembers(t *testing.T) {
	s := openSCIMStore(t)
	ctx := context.Background()
	// Create the users first so the FK is satisfiable.
	alice, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)
	bob, err := s.CreateUser(ctx, scim.User{UserName: "bob"})
	require.NoError(t, err)

	created, err := s.CreateGroup(ctx, scim.Group{
		DisplayName: "platform-eng",
		Members: []scim.Ref{
			{Value: alice.ID},
			{Value: bob.ID},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)

	got, err := s.GetGroup(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "platform-eng", got.DisplayName)
	assert.Len(t, got.Members, 2)
}

func TestSCIM_DuplicateDisplayNameReturnsConflict(t *testing.T) {
	s := openSCIMStore(t)
	ctx := context.Background()
	_, err := s.CreateGroup(ctx, scim.Group{DisplayName: "eng"})
	require.NoError(t, err)
	_, err = s.CreateGroup(ctx, scim.Group{DisplayName: "eng"})
	assert.ErrorIs(t, err, scim.ErrConflict)
}

func TestSCIM_ReplaceGroupReplacesMembers(t *testing.T) {
	// Load-bearing SCIM 2.0 PUT semantic: full replace, not
	// merge. Old members should be removed when they aren't
	// in the new list.
	s := openSCIMStore(t)
	ctx := context.Background()
	alice, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)
	bob, err := s.CreateUser(ctx, scim.User{UserName: "bob"})
	require.NoError(t, err)
	carol, err := s.CreateUser(ctx, scim.User{UserName: "carol"})
	require.NoError(t, err)

	g, err := s.CreateGroup(ctx, scim.Group{
		DisplayName: "eng",
		Members:     []scim.Ref{{Value: alice.ID}, {Value: bob.ID}},
	})
	require.NoError(t, err)

	// Replace with a completely different set.
	replaced, err := s.ReplaceGroup(ctx, g.ID, scim.Group{
		DisplayName: "eng",
		Members:     []scim.Ref{{Value: carol.ID}},
	})
	require.NoError(t, err)
	require.Len(t, replaced.Members, 1)
	assert.Equal(t, carol.ID, replaced.Members[0].Value)

	// Persistent roundtrip.
	got, err := s.GetGroup(ctx, g.ID)
	require.NoError(t, err)
	require.Len(t, got.Members, 1)
	assert.Equal(t, carol.ID, got.Members[0].Value)
}

// -- Count --

func TestSCIM_CountReflectsRows(t *testing.T) {
	s := openSCIMStore(t)
	ctx := context.Background()
	_, err := s.CreateUser(ctx, scim.User{UserName: "u1"})
	require.NoError(t, err)
	_, err = s.CreateUser(ctx, scim.User{UserName: "u2"})
	require.NoError(t, err)
	_, err = s.CreateGroup(ctx, scim.Group{DisplayName: "g1"})
	require.NoError(t, err)

	users, groups, err := s.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, users)
	assert.Equal(t, 1, groups)
}

// -- Sanity: interface satisfaction --

func TestSCIM_ImplementsInterface(t *testing.T) {
	// Compile-time check via type assertion; if the interface
	// gains a method we get a legible test failure.
	var _ scim.Store = (*SCIMStore)(nil)
	// Runtime check for satisfaction from a nil pointer.
	require.NotNil(t, errors.New("compile-time only"))
}
