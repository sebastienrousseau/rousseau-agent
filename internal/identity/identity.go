// Package identity maps per-transport sender IDs to a stable rousseau
// identity so a single conversation can span multiple channels
// ("start on WhatsApp, continue on Slack").
//
// Every transport router calls [Resolver.Resolve] before looking up
// the session — the resulting IdentityID is the primary key for
// session storage instead of the raw JID. Identities carry an
// ordered list of Handles, each `<transport>:<sender>` pair the
// identity has been linked to.
//
// The link is bidirectional: a message from a linked handle looks up
// the identity via [Resolver.Resolve]; a reply lands back on that
// handle by consulting [Resolver.HandlesFor] (typically only one
// handle per identity per transport, but the schema allows more —
// same user, multiple linked WhatsApp accounts, etc.).
package identity

import (
	"context"
	"errors"
	"time"
)

// ID is the stable identity identifier (typically a UUID or a
// derivative of the first linked handle).
type ID string

// Handle is one `<transport>:<sender>` pair linked to an identity.
type Handle struct {
	Transport  string
	Sender     string
	VerifiedAt time.Time
}

// Identity aggregates every handle linked to a stable rousseau user.
type Identity struct {
	ID             ID
	PrimaryDisplay string
	Handles        []Handle
	CreatedAt      time.Time
}

// ErrNotLinked is returned by [Resolver.Resolve] when a
// (transport, sender) pair has no identity yet. Callers typically
// bootstrap the identity with [Resolver.Provision] on the first
// message from a new handle.
var ErrNotLinked = errors.New("identity: handle not linked to any identity")

// Resolver is the contract the transport router speaks. Implementations
// must be safe for concurrent use.
type Resolver interface {
	// Resolve returns the identity ID linked to (transport, sender),
	// or ErrNotLinked if unlinked.
	Resolve(ctx context.Context, transport, sender string) (ID, error)
	// Provision creates a fresh identity for (transport, sender)
	// and returns its ID. Idempotent — a second call on the same
	// pair returns the same ID.
	Provision(ctx context.Context, transport, sender, display string) (ID, error)
	// Link attaches an additional handle to an existing identity.
	// Returns ErrAlreadyLinked if the pair is already bound to any
	// identity (whether the same one or a different one).
	Link(ctx context.Context, id ID, transport, sender string) error
	// Unlink removes a handle from its identity. When the last
	// handle is removed the identity itself becomes orphaned but is
	// NOT deleted — session state is preserved so a future re-link
	// picks up where it left off.
	Unlink(ctx context.Context, transport, sender string) error
	// Get returns the full Identity record.
	Get(ctx context.Context, id ID) (Identity, error)
	// HandlesFor returns every handle linked to id.
	HandlesFor(ctx context.Context, id ID) ([]Handle, error)
}

// ErrAlreadyLinked is returned by [Resolver.Link] when the given
// (transport, sender) is already bound to some identity.
var ErrAlreadyLinked = errors.New("identity: handle already linked")

// ErrIdentityNotFound is returned by [Resolver.Get] when the ID is unknown.
var ErrIdentityNotFound = errors.New("identity: not found")
