package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/scim"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// TestNewJIDMap_ErrorFromClosedStore hits the schema-apply error path
// by handing NewJIDMap a Store whose db has already been closed.
func TestNewJIDMap_ErrorFromClosedStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewJIDMap(context.Background(), s)
	assert.Error(t, err)
}

func TestNewCronStore_ErrorFromClosedStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewCronStore(context.Background(), s)
	assert.Error(t, err)
}

func TestNewOAuthTokens_ErrorFromClosedStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewOAuthTokens(context.Background(), s)
	assert.Error(t, err)
}

func TestNewRecallVectors_ErrorFromClosedStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewRecallVectors(context.Background(), s)
	assert.Error(t, err)
}

// TestJIDMap_GetPutErrorPaths exercises the wrapper error branches
// by driving the store against a closed db.
func TestJIDMap_GetPutErrorPaths(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	m, err := NewJIDMap(context.Background(), s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())

	_, _, err = m.Get(context.Background(), "u")
	assert.Error(t, err)
	assert.Error(t, m.Put(context.Background(), "u", "sess"))
}

func TestOAuth_GetPutDeleteListError(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	o, err := NewOAuthTokens(context.Background(), s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())

	_, _, err = o.Get(context.Background(), "p", "a")
	assert.Error(t, err)
	assert.Error(t, o.Put(context.Background(), "p", "a", []byte("x")))
	assert.Error(t, o.Delete(context.Background(), "p", "a"))
	_, err = o.List(context.Background())
	assert.Error(t, err)
	assert.Error(t, o.Iterate(context.Background(), func(string, string, []byte) error { return nil }))
}

func TestRecallVectors_ErrorsOnClosedDB(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	rv, err := NewRecallVectors(context.Background(), s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())

	assert.Error(t, rv.Put(context.Background(), VectorRow{
		SessionID: "s", MessageID: 1,
		Embedding: []byte{0, 0, 0, 0},
		CreatedAt: time.Now().UTC(),
	}))
	_, err = rv.Count(context.Background())
	assert.Error(t, err)
	_, err = rv.All(context.Background())
	assert.Error(t, err)
	_, err = rv.Since(context.Background(), time.Time{})
	assert.Error(t, err)
	_, err = rv.PurgeOlderThan(context.Background(), time.Now())
	assert.Error(t, err)
}

func TestSearch_ErrorsOnClosedDB(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = s.Search(context.Background(), "hi", SearchOptions{})
	assert.Error(t, err)
	_, err = s.RecentSessions(context.Background(), 5)
	assert.Error(t, err)
	assert.Error(t, s.EnsureSearch(context.Background()))
}

// -- ListBySender: cover the empty-sender + limit>0 + multi-row paths.

func TestListBySender_EmptySenderReturnsNil(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup
	got, err := s.ListBySender(context.Background(), "", 10)
	require.NoError(t, err)
	assert.Nil(t, got, "empty sender must return nil, never all rows")
}

func TestListBySender_HonoursLimitAndReturnsMultipleRows(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup

	// Seed 3 sessions for the same sender + 1 for another. Use the
	// public Save path so payload NOT NULL + schema drift stay honest.
	seed := func(id, sender, title string) {
		require.NoError(t, s.Save(ctx, &agent.Session{
			ID: id, Sender: sender, Title: title,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}))
	}
	seed("s1", "alice", "one")
	seed("s2", "alice", "two")
	seed("s3", "alice", "three")
	seed("s4", "bob", "four")

	// limit=2 → should return exactly 2 rows for alice.
	got, err := s.ListBySender(ctx, "alice", 2)
	require.NoError(t, err)
	assert.Len(t, got, 2, "limit must cap result count")

	// No limit → all 3 alice rows.
	got, err = s.ListBySender(ctx, "alice", 0)
	require.NoError(t, err)
	assert.Len(t, got, 3, "limit=0 must return all matches")

	// Different sender → 1 row.
	got, err = s.ListBySender(ctx, "bob", 0)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestListBySender_ErrorOnClosedDB(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = s.ListBySender(context.Background(), "alice", 10)
	assert.Error(t, err)
}

// -- SCIM: DeleteUser / Count / ReplaceGroup / isUniqueViolation coverage.

func newSCIMForTest(t *testing.T) *SCIMStore {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	scim, err := NewSCIMStore(context.Background(), s)
	require.NoError(t, err)
	return scim
}

func TestSCIM_Count_EmptyStore(t *testing.T) {
	s := newSCIMForTest(t)
	users, groups, err := s.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, users)
	assert.Equal(t, 0, groups)
}

func TestSCIM_Count_AfterCreate(t *testing.T) {
	s := newSCIMForTest(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, scim.User{ID: "u1", UserName: "alice", Active: true})
	require.NoError(t, err)
	_ = u
	users, _, err := s.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, users)
}

func TestSCIM_DeleteUser_RemovesIt(t *testing.T) {
	s := newSCIMForTest(t)
	ctx := context.Background()
	_, err := s.CreateUser(ctx, scim.User{ID: "u1", UserName: "alice", Active: true})
	require.NoError(t, err)
	require.NoError(t, s.DeleteUser(ctx, "u1"))
	// Verify it's gone via Count.
	users, _, err := s.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, users)
}

func TestSCIM_DeleteUser_UnknownIDIsIdempotent(t *testing.T) {
	// Freezes the idempotent-delete contract: SCIM DELETE-of-missing
	// is allowed per RFC 7644 §3.6 (server SHOULD respond 404). This
	// impl treats it as a no-op — the SCIM handler layer surfaces 404.
	s := newSCIMForTest(t)
	err := s.DeleteUser(context.Background(), "nope")
	assert.NoError(t, err)
}

func TestSCIM_CreateUser_DuplicateUsernameIsUniqueViolation(t *testing.T) {
	s := newSCIMForTest(t)
	ctx := context.Background()
	_, err := s.CreateUser(ctx, scim.User{ID: "u1", UserName: "alice"})
	require.NoError(t, err)
	// Same username, different id → isUniqueViolation branch fires
	// and the error is translated to ErrConflict.
	_, err = s.CreateUser(ctx, scim.User{ID: "u2", UserName: "alice"})
	require.Error(t, err)
}

func TestSCIM_ReplaceGroup_UpdatesFields(t *testing.T) {
	s := newSCIMForTest(t)
	ctx := context.Background()
	// Need a user for group membership.
	_, err := s.CreateUser(ctx, scim.User{ID: "u1", UserName: "alice"})
	require.NoError(t, err)
	// Create a group with the user.
	g, err := s.CreateGroup(ctx, scim.Group{
		ID: "g1", DisplayName: "team-a", Members: []scim.Ref{{Value: "u1"}},
	})
	require.NoError(t, err)
	_ = g
	// Rename + change membership via ReplaceGroup.
	replaced, err := s.ReplaceGroup(ctx, "g1", scim.Group{
		DisplayName: "team-a-renamed",
	})
	require.NoError(t, err)
	assert.Equal(t, "team-a-renamed", replaced.DisplayName)
}

func TestSCIM_ReplaceGroup_UnknownIDErrors(t *testing.T) {
	s := newSCIMForTest(t)
	_, err := s.ReplaceGroup(context.Background(), "nope", scim.Group{DisplayName: "x"})
	assert.Error(t, err)
}

// -- SSO bindings: cover Unbind not-found path.

func TestSSOBindings_UnbindUnknownReturnsNoError(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup
	b, err := NewSSOBindings(context.Background(), s)
	require.NoError(t, err)
	// Delete of a nonexistent binding is a no-op — this is
	// deliberate so /logout is idempotent.
	err = b.Unbind(context.Background(), "wa", "unknown-jid")
	assert.NoError(t, err)
}

// -- NewSSOBindings / NewSCIMStore errors on closed store.

func TestNewSSOBindings_ErrorFromClosedStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewSSOBindings(context.Background(), s)
	assert.Error(t, err)
}

func TestNewSCIMStore_ErrorFromClosedStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewSCIMStore(context.Background(), s)
	assert.Error(t, err)
}

// -- NewAuditChainState error path.

func TestNewAuditChainState_ErrorFromClosedStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = NewAuditChainState(context.Background(), s)
	assert.Error(t, err)
}

// -- SCIM CreateGroup: error path when a member is missing.

func TestSCIM_CreateGroup_MissingMemberReturnsError(t *testing.T) {
	s := newSCIMForTest(t)
	// Group with a member id that doesn't correspond to any user
	// → FK violation. sqlite reports as constraint failed.
	_, err := s.CreateGroup(context.Background(), scim.Group{
		ID: "g1", DisplayName: "team-a",
		Members: []scim.Ref{{Value: "does-not-exist"}},
	})
	assert.Error(t, err, "member referencing unknown user must fail")
}

// -- SCIM ReplaceUser: cover the update path + preserve-created-at.

func TestSCIM_ReplaceUser_UpdatesFields(t *testing.T) {
	s := newSCIMForTest(t)
	ctx := context.Background()
	orig, err := s.CreateUser(ctx, scim.User{
		ID: "u1", UserName: "alice",
		Emails: []scim.Email{{Value: "alice@a.example", Primary: true}},
	})
	require.NoError(t, err)
	// ReplaceUser: change username + emails.
	updated, err := s.ReplaceUser(ctx, "u1", scim.User{
		ID: "u1", UserName: "alice2",
		Emails: []scim.Email{{Value: "alice@b.example", Primary: true}},
	})
	require.NoError(t, err)
	assert.Equal(t, "alice2", updated.UserName)
	assert.Equal(t, "alice@b.example", updated.Emails[0].Value)
	// Created timestamp must not regress.
	assert.False(t, updated.Meta.Created.After(orig.Meta.Created.Add(time.Second)))
}

func TestSCIM_ReplaceUser_UnknownIDErrors(t *testing.T) {
	s := newSCIMForTest(t)
	_, err := s.ReplaceUser(context.Background(), "nope", scim.User{UserName: "x"})
	assert.Error(t, err)
}

// -- SSO bindings: Bind + Lookup happy path + Count.

func TestSSOBindings_BindLookupCount(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup
	b, err := NewSSOBindings(context.Background(), s)
	require.NoError(t, err)
	ctx := context.Background()

	// Bind alice → sub=alice-oidc-id.
	require.NoError(t, b.Bind(ctx, "wa", "447906009073", sso.Identity{
		Subject: "alice-oidc-id", Email: "alice@example.com",
		DisplayName: "Alice",
	}, time.Now().Add(time.Hour)))

	// Lookup surfaces the identity.
	got, ok, err := b.Lookup(ctx, "wa", "447906009073")
	require.NoError(t, err)
	require.True(t, ok, "just-bound identity must resolve")
	assert.Equal(t, "alice-oidc-id", got.Subject)
	assert.Equal(t, "alice@example.com", got.Email)

	// Count surfaces the one binding.
	n, err := b.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// Unbind + Count returns to 0.
	require.NoError(t, b.Unbind(ctx, "wa", "447906009073"))
	n, err = b.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

// -- reliability_store closed-db error paths.

func TestReliabilityStore_LoadSinceErrorOnClosedDB(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	rs, err := NewReliabilitySampleStore(context.Background(), s, nil)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = rs.LoadSince(context.Background(), time.Now().Add(-time.Hour))
	assert.Error(t, err)
}

func TestReliabilityStore_PruneBeforeErrorOnClosedDB(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	rs, err := NewReliabilitySampleStore(context.Background(), s, nil)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, err = rs.PruneBefore(context.Background(), time.Now())
	assert.Error(t, err)
}

// -- audit_chain_state closed-db error paths.

func TestAuditChainState_LoadErrorOnClosedDB(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	acs, err := NewAuditChainState(context.Background(), s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, _, err = acs.Load(context.Background())
	assert.Error(t, err)
}

func TestAuditChainState_SaveErrorOnClosedDB(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	acs, err := NewAuditChainState(context.Background(), s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	assert.Error(t, acs.Save(context.Background(), 1, "abc123"))
}

// -- identity: newIdentityID uniqueness + format.

func TestNewIdentityID_ProducesUniqueValues(t *testing.T) {
	// Freezes the "no duplicates in a small burst" invariant. The
	// implementation uses random bytes so collisions are astronomical
	// but we still assert the surface behaviour.
	seen := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		id := newIdentityID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id at i=%d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

// -- SCIM.Count error on closed db.

func TestSCIM_Count_ErrorOnClosedDB(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	scimStore, err := NewSCIMStore(context.Background(), s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	_, _, err = scimStore.Count(context.Background())
	assert.Error(t, err)
}

// -- SCIM.DeleteUser error on closed db.

func TestSCIM_DeleteUser_ErrorOnClosedDB(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	scimStore, err := NewSCIMStore(context.Background(), s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	err = scimStore.DeleteUser(context.Background(), "u1")
	assert.Error(t, err)
}
