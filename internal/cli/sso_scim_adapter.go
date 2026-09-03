package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/scim"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// scimDirectoryStore adapts a SCIM-backed store to the
// [sso.DirectoryStore] contract so [OIDCDirectory.
// ResolveTransportID] can hand back a real Identity when SCIM
// is configured.
//
// Holds the SCIM store as two interfaces: the shared scim.Store
// (LookupUserByExternalID lives there) and a groupNames helper
// interface for UserGroupNames (which is NOT on scim.Store).
// Both sqlite.SCIMStore and postgres.SCIMStore satisfy both.
//
// Kept as an anonymous adapter in cli (not exported) — the
// coupling is one-way (sso stays independent of scim; the
// daemon knows both). A future backend (LDAP, Azure Graph,
// static YAML) writes its own adapter.
type scimDirectoryStore struct {
	store  scim.Store
	groups SCIMGroupNamesStore
}

// newSCIMDirectoryStore is retained for backwards compatibility
// with existing tests that hold a concrete SCIM store. New
// callers use newSCIMDirectoryStoreFromIface which is
// driver-agnostic.
func newSCIMDirectoryStore(store scim.Store) *scimDirectoryStore {
	return newSCIMDirectoryStoreFromIface(store, scimGroupNamesFrom(store))
}

// newSCIMDirectoryStoreFromIface builds the adapter from a
// scim.Store and its matching SCIMGroupNamesStore. Called by the
// daemon assembly with both derived from the same concrete
// backend (sqlite or postgres) so the groups method sees the
// same data.
func newSCIMDirectoryStoreFromIface(store scim.Store, groups SCIMGroupNamesStore) *scimDirectoryStore {
	return &scimDirectoryStore{store: store, groups: groups}
}

// ResolveExternalID satisfies [sso.DirectoryStore]. Returns the
// SCIM-provisioned user's identity (with hydrated group
// memberships) or [sso.ErrNotFound].
//
// # Field mapping
//
// SCIM user → sso.Identity:
//
//	Subject     ← user.ID (SCIM-assigned UUID; stable across
//	              renames on the IdP side)
//	Email       ← first primary email, else first email
//	DisplayName ← userName (fallback: name.formatted)
//	Groups      ← displayNames from scim_group_members (via
//	              [SCIMStore.UserGroupNames])
//	ExpiresAt   ← unset (SCIM doesn't carry session expiry;
//	              downstream binding TTLs govern re-auth)
func (a *scimDirectoryStore) ResolveExternalID(ctx context.Context, externalID string) (sso.Identity, error) {
	if a == nil || a.store == nil {
		return sso.Identity{}, sso.ErrNotFound
	}
	u, err := a.store.LookupUserByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, scim.ErrNotFound) {
			return sso.Identity{}, sso.ErrNotFound
		}
		return sso.Identity{}, fmt.Errorf("scim resolve: %w", err)
	}
	// Group lookup is optional — a nil groups accessor means the
	// backend doesn't expose UserGroupNames (should never happen
	// with sqlite/postgres, both of which do, but tests that
	// wrap a bare scim.Store fall through cleanly). A store
	// hiccup on group lookup is fail-CLOSED — we return the
	// error rather than an identity with stale (empty) groups
	// so RBAC / OPA don't see a partial Identity.
	var groups []string
	if a.groups != nil {
		var gerr error
		groups, gerr = a.groups.UserGroupNames(ctx, u.ID)
		if gerr != nil {
			return sso.Identity{}, fmt.Errorf("scim group lookup: %w", gerr)
		}
	}
	id := sso.Identity{
		Subject:     u.ID,
		Groups:      groups,
		DisplayName: displayNameFrom(u),
		Email:       primaryEmail(u),
	}
	return id, nil
}

// displayNameFrom picks the best user-facing name from a SCIM
// User. Priority: name.formatted > userName. Never returns "".
func displayNameFrom(u scim.User) string {
	if u.Name != nil && u.Name.Formatted != "" {
		return u.Name.Formatted
	}
	return u.UserName
}

// primaryEmail returns the first primary email, or the first
// email if none is marked primary. Empty when the user has no
// emails.
func primaryEmail(u scim.User) string {
	for _, e := range u.Emails {
		if e.Primary && e.Value != "" {
			return e.Value
		}
	}
	if len(u.Emails) > 0 {
		return u.Emails[0].Value
	}
	return ""
}

// Compile-time interface satisfaction.
var _ sso.DirectoryStore = (*scimDirectoryStore)(nil)
