package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/scim"
)

// openSCIMTest opens a SCIMStore on the CI/dev Postgres, applies
// the schema, and truncates all three SCIM tables so each test
// starts clean. Guarded on ROUSSEAU_TEST_POSTGRES_URL like the
// other integration tests in this package.
//
// TRUNCATE scim_users, scim_groups CASCADE cleans memberships
// via the FK — no separate TRUNCATE needed.
func openSCIMTest(t *testing.T) (*SCIMStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	s, err := NewSCIMStore(ctx, store)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `TRUNCATE TABLE scim_users, scim_groups CASCADE`)
	require.NoError(t, err)
	return s, ctx
}

// -- unit-only (no DB) --

func TestNewSCIMStore_SchemaIdempotent(t *testing.T) {
	// Compile-check the const we ship — an accidental rename or
	// missing FK / index would break the daemon at boot.
	assert.Contains(t, scimSchema, "CREATE TABLE IF NOT EXISTS scim_users")
	assert.Contains(t, scimSchema, "CREATE TABLE IF NOT EXISTS scim_groups")
	assert.Contains(t, scimSchema, "CREATE TABLE IF NOT EXISTS scim_group_members")
	assert.Contains(t, scimSchema, "body        JSONB NOT NULL")
	assert.Contains(t, scimSchema, "REFERENCES scim_groups(id) ON DELETE CASCADE")
	assert.Contains(t, scimSchema, "REFERENCES scim_users(id)  ON DELETE CASCADE")
	assert.Contains(t, scimSchema, "idx_scim_group_members_user")
}

func TestIsUniqueViolation_NilAndUnrelatedErrorsReturnFalse(t *testing.T) {
	assert.False(t, isUniqueViolation(nil))
	assert.False(t, isUniqueViolation(assert.AnError))
}

// -- integration --

func TestIntegration_SCIM_CreateGetUser(t *testing.T) {
	s, ctx := openSCIMTest(t)

	u, err := s.CreateUser(ctx, scim.User{UserName: "alice", ExternalID: "ext-alice"})
	require.NoError(t, err)
	assert.NotEmpty(t, u.ID, "SCIM SP must assign an ID when caller omits it")
	assert.True(t, u.Active, "Active defaults to true per SCIM 2.0 §3")

	got, err := s.GetUser(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "alice", got.UserName)
	assert.Equal(t, "ext-alice", got.ExternalID)
}

func TestIntegration_SCIM_CreateUserDuplicateUserNameConflict(t *testing.T) {
	// Load-bearing: an IdP retrying a provisioning call for an
	// already-existing userName must see ErrConflict, not a
	// generic 500. The HTTP handler translates ErrConflict to
	// SCIM's "uniqueness" scimType.
	s, ctx := openSCIMTest(t)
	_, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)

	_, err = s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.ErrorIs(t, err, scim.ErrConflict)
}

func TestIntegration_SCIM_CreateUserDuplicateExternalIDConflict(t *testing.T) {
	// Same pattern for externalId — the second unique column.
	s, ctx := openSCIMTest(t)
	_, err := s.CreateUser(ctx, scim.User{UserName: "u1", ExternalID: "ext-1"})
	require.NoError(t, err)

	_, err = s.CreateUser(ctx, scim.User{UserName: "u2", ExternalID: "ext-1"})
	require.ErrorIs(t, err, scim.ErrConflict)
}

func TestIntegration_SCIM_CreateUserMultipleNullExternalIDsAllowed(t *testing.T) {
	// Postgres UNIQUE treats multiple NULLs as distinct — matches
	// SQLite's behaviour. IdPs that never supply externalId (rare
	// but valid per spec) must be able to provision multiple users.
	s, ctx := openSCIMTest(t)
	_, err := s.CreateUser(ctx, scim.User{UserName: "u1"})
	require.NoError(t, err)
	_, err = s.CreateUser(ctx, scim.User{UserName: "u2"})
	require.NoError(t, err, "multiple NULL externalId must not collide")
}

func TestIntegration_SCIM_GetUserMissingReturnsErrNotFound(t *testing.T) {
	s, ctx := openSCIMTest(t)
	_, err := s.GetUser(ctx, "does-not-exist")
	require.ErrorIs(t, err, scim.ErrNotFound)
}

func TestIntegration_SCIM_ListUsersWithFilterAndPagination(t *testing.T) {
	s, ctx := openSCIMTest(t)
	for _, name := range []string{"alice", "bob", "carol"} {
		_, err := s.CreateUser(ctx, scim.User{UserName: name})
		require.NoError(t, err)
	}
	// Filter: exact userName match.
	users, total, err := s.ListUsers(ctx, "bob", 1, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, users, 1)
	assert.Equal(t, "bob", users[0].UserName)

	// Pagination: startIndex=2 count=1 → the second user
	// (alphabetical order — SCIM SPs must be deterministic).
	page, _, err := s.ListUsers(ctx, "", 2, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "bob", page[0].UserName)
}

func TestIntegration_SCIM_ReplaceUserMissingReturnsErrNotFound(t *testing.T) {
	// Existence-then-update contract: missing ID surfaces as
	// ErrNotFound, not a silent no-op.
	s, ctx := openSCIMTest(t)
	_, err := s.ReplaceUser(ctx, "not-there", scim.User{UserName: "x"})
	require.ErrorIs(t, err, scim.ErrNotFound)
}

func TestIntegration_SCIM_DeleteUserIdempotent(t *testing.T) {
	// SCIM 2.0 §3.6 recommends idempotent delete so IdPs can
	// retry safely. Second delete of the same ID must return nil.
	s, ctx := openSCIMTest(t)
	u, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)
	require.NoError(t, s.DeleteUser(ctx, u.ID))
	require.NoError(t, s.DeleteUser(ctx, u.ID))
}

func TestIntegration_SCIM_LookupByExternalIDEmptyReturnsErrNotFound(t *testing.T) {
	// Empty externalID short-circuits to ErrNotFound to avoid
	// unifying every no-externalId user via a single SELECT
	// WHERE external_id = ''. Matches the SQLite driver.
	s, ctx := openSCIMTest(t)
	_, err := s.LookupUserByExternalID(ctx, "")
	require.ErrorIs(t, err, scim.ErrNotFound)
}

func TestIntegration_SCIM_LookupByExternalIDRoundtrip(t *testing.T) {
	s, ctx := openSCIMTest(t)
	u, err := s.CreateUser(ctx, scim.User{UserName: "alice", ExternalID: "ext-alice"})
	require.NoError(t, err)

	got, err := s.LookupUserByExternalID(ctx, "ext-alice")
	require.NoError(t, err)
	assert.Equal(t, u.ID, got.ID)
}

// -- Groups --

func TestIntegration_SCIM_CreateGroupWithMembers(t *testing.T) {
	// Group + memberships must land atomically. Pinned via a
	// happy-path CreateGroup followed by UserGroupNames from the
	// member's perspective.
	s, ctx := openSCIMTest(t)
	alice, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)
	bob, err := s.CreateUser(ctx, scim.User{UserName: "bob"})
	require.NoError(t, err)

	g, err := s.CreateGroup(ctx, scim.Group{
		DisplayName: "eng",
		Members:     []scim.Ref{{Value: alice.ID}, {Value: bob.ID}},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, g.ID)

	groups, err := s.UserGroupNames(ctx, alice.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"eng"}, groups)
}

func TestIntegration_SCIM_CreateGroupDuplicateDisplayNameConflict(t *testing.T) {
	s, ctx := openSCIMTest(t)
	_, err := s.CreateGroup(ctx, scim.Group{DisplayName: "eng"})
	require.NoError(t, err)
	_, err = s.CreateGroup(ctx, scim.Group{DisplayName: "eng"})
	require.ErrorIs(t, err, scim.ErrConflict)
}

func TestIntegration_SCIM_ReplaceGroupUpdatesMembers(t *testing.T) {
	// ReplaceGroup wipes the old membership set and replaces
	// with the new one atomically. Verify the DELETE-then-INSERT
	// path preserves the "current members" invariant.
	s, ctx := openSCIMTest(t)
	alice, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)
	bob, err := s.CreateUser(ctx, scim.User{UserName: "bob"})
	require.NoError(t, err)

	g, err := s.CreateGroup(ctx, scim.Group{
		DisplayName: "eng",
		Members:     []scim.Ref{{Value: alice.ID}},
	})
	require.NoError(t, err)

	// Replace with only bob.
	_, err = s.ReplaceGroup(ctx, g.ID, scim.Group{
		DisplayName: "eng",
		Members:     []scim.Ref{{Value: bob.ID}},
	})
	require.NoError(t, err)

	// alice must no longer be a member.
	aliceGroups, err := s.UserGroupNames(ctx, alice.ID)
	require.NoError(t, err)
	assert.Empty(t, aliceGroups)

	// bob must be a member now.
	bobGroups, err := s.UserGroupNames(ctx, bob.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"eng"}, bobGroups)
}

func TestIntegration_SCIM_ReplaceGroupMissingReturnsErrNotFound(t *testing.T) {
	s, ctx := openSCIMTest(t)
	_, err := s.ReplaceGroup(ctx, "not-there", scim.Group{DisplayName: "x"})
	require.ErrorIs(t, err, scim.ErrNotFound)
}

func TestIntegration_SCIM_DeleteGroupCascadesMemberships(t *testing.T) {
	// FK ON DELETE CASCADE proves out: deleting a group wipes
	// its membership rows so UserGroupNames returns empty for
	// former members. Pinned so a Postgres refactor that
	// weakens the FK to NO ACTION would fail this test.
	s, ctx := openSCIMTest(t)
	alice, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)
	g, err := s.CreateGroup(ctx, scim.Group{DisplayName: "eng", Members: []scim.Ref{{Value: alice.ID}}})
	require.NoError(t, err)

	require.NoError(t, s.DeleteGroup(ctx, g.ID))
	aliceGroups, err := s.UserGroupNames(ctx, alice.ID)
	require.NoError(t, err)
	assert.Empty(t, aliceGroups, "memberships must cascade with the group")
}

func TestIntegration_SCIM_DeleteUserCascadesMemberships(t *testing.T) {
	// Symmetric to the group cascade — deleting a user wipes
	// their membership rows. Prevents a Get on the group from
	// silently returning stale members.
	s, ctx := openSCIMTest(t)
	alice, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)
	_, err = s.CreateGroup(ctx, scim.Group{DisplayName: "eng", Members: []scim.Ref{{Value: alice.ID}}})
	require.NoError(t, err)

	require.NoError(t, s.DeleteUser(ctx, alice.ID))
	groups, err := s.UserGroupNames(ctx, alice.ID)
	require.NoError(t, err)
	assert.Empty(t, groups)
}

func TestIntegration_SCIM_ListGroupsWithFilter(t *testing.T) {
	s, ctx := openSCIMTest(t)
	for _, name := range []string{"eng", "sre", "product"} {
		_, err := s.CreateGroup(ctx, scim.Group{DisplayName: name})
		require.NoError(t, err)
	}
	groups, total, err := s.ListGroups(ctx, "sre", 1, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, groups, 1)
	assert.Equal(t, "sre", groups[0].DisplayName)
}

func TestIntegration_SCIM_UserGroupNamesEmptyReturnsNil(t *testing.T) {
	// Empty userID short-circuits to nil so callers threading
	// through unauthenticated codepaths don't accidentally hit
	// the DB. Matches the SQLite driver.
	s, ctx := openSCIMTest(t)
	got, err := s.UserGroupNames(ctx, "")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestIntegration_SCIM_UserGroupNamesSortedAlphabetically(t *testing.T) {
	// SSO adapter maps groups → sso.Identity.Groups; RBAC / OPA
	// downstream policies key on this list. Alphabetical order
	// keeps the observable list stable across driver / clock
	// / row-insertion variations.
	s, ctx := openSCIMTest(t)
	alice, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)
	// Create groups out of alphabetical insertion order.
	for _, name := range []string{"zeta", "alpha", "mu"} {
		_, err := s.CreateGroup(ctx, scim.Group{DisplayName: name, Members: []scim.Ref{{Value: alice.ID}}})
		require.NoError(t, err)
	}

	got, err := s.UserGroupNames(ctx, alice.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "mu", "zeta"}, got)
}

func TestIntegration_SCIM_Count(t *testing.T) {
	s, ctx := openSCIMTest(t)
	for _, name := range []string{"alice", "bob"} {
		_, err := s.CreateUser(ctx, scim.User{UserName: name})
		require.NoError(t, err)
	}
	for _, name := range []string{"eng"} {
		_, err := s.CreateGroup(ctx, scim.Group{DisplayName: name})
		require.NoError(t, err)
	}
	users, groups, err := s.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, users)
	assert.Equal(t, 1, groups)
}
