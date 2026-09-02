package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/scim"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// scimDirectoryStore adapts the SCIM-backed sqlite store to
// the [sso.DirectoryStore] contract so [OIDCDirectory.
// ResolveTransportID] can hand back a real Identity when SCIM
// is configured.
//
// Kept as an anonymous adapter in cli (not exported) — the
// coupling is one-way (sso stays independent of scim; the
// daemon knows both). A future backend (LDAP, Azure Graph,
// static YAML) writes its own adapter.
type scimDirectoryStore struct {
	store *sqlitestore.SCIMStore
}

func newSCIMDirectoryStore(store *sqlitestore.SCIMStore) *scimDirectoryStore {
	return &scimDirectoryStore{store: store}
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
	groups, err := a.store.UserGroupNames(ctx, u.ID)
	if err != nil {
		// A store hiccup on group lookup shouldn't hide an
		// otherwise-found user — return the identity without
		// groups, log at Warn upstream. But we haven't wired a
		// logger into this adapter; the sso layer will log the
		// wrapped error if it propagates. For fail-CLOSED on
		// governance: return the error so RBAC / OPA don't
		// see a stale-groups Identity.
		return sso.Identity{}, fmt.Errorf("scim group lookup: %w", err)
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
