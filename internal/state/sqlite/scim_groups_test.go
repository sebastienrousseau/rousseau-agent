package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/scim"
)

// TestSCIM_GroupLifecycle covers listing, membership lookup, replace
// and delete for groups, which drive RBAC group checks.
func TestSCIM_GroupLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openSCIMStore(t)
	alice, err := s.CreateUser(ctx, scim.User{UserName: "alice"})
	require.NoError(t, err)
	bob, err := s.CreateUser(ctx, scim.User{UserName: "bob"})
	require.NoError(t, err)

	eng, err := s.CreateGroup(ctx, scim.Group{DisplayName: "engineering", Members: []scim.Ref{{Value: alice.ID}}})
	require.NoError(t, err)
	_, err = s.CreateGroup(ctx, scim.Group{DisplayName: "sales", Members: []scim.Ref{{Value: bob.ID}}})
	require.NoError(t, err)

	all, total, err := s.ListGroups(ctx, "", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, all, 2)
	one, total, err := s.ListGroups(ctx, "engineering", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, one, 1)
	assert.Equal(t, eng.ID, one[0].ID)
	page, _, err := s.ListGroups(ctx, "", 2, 1)
	require.NoError(t, err)
	assert.Len(t, page, 1, "startIndex/count page the result")

	names, err := s.UserGroupNames(ctx, alice.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"engineering"}, names)

	eng.Members = []scim.Ref{{Value: alice.ID}, {Value: bob.ID}}
	_, err = s.ReplaceGroup(ctx, eng.ID, eng)
	require.NoError(t, err)
	names, err = s.UserGroupNames(ctx, bob.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"engineering", "sales"}, names)

	require.NoError(t, s.DeleteGroup(ctx, eng.ID))
	names, err = s.UserGroupNames(ctx, alice.ID)
	require.NoError(t, err)
	assert.Empty(t, names, "deleting a group drops its memberships")
	assert.NoError(t, s.DeleteGroup(ctx, eng.ID), "delete is idempotent (SCIM 2.0 §3.6)")
	_, err = s.ReplaceGroup(ctx, "missing", scim.Group{DisplayName: "x"})
	assert.Error(t, err)
}
