// Package state defines the persistence contract for Sessions and
// exposes a default SQLite-backed implementation via state/sqlite.
package state

import (
	"context"
	"errors"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// ErrNotFound is returned when a Session cannot be located.
var ErrNotFound = errors.New("state: session not found")

// Store persists Sessions across process lifetimes. Implementations MUST
// be safe for concurrent use.
type Store interface {
	// Save writes a Session, creating it or replacing its content.
	Save(ctx context.Context, s *agent.Session) error
	// Load returns the Session identified by id, or ErrNotFound.
	Load(ctx context.Context, id string) (*agent.Session, error)
	// List returns Session summaries newest-first, capped at limit
	// (0 disables the cap).
	List(ctx context.Context, limit int) ([]Summary, error)
	// Delete removes the Session identified by id. Deleting a missing
	// Session is not an error.
	Delete(ctx context.Context, id string) error
	// Close releases underlying resources.
	Close() error
}

// Summary is a compact Session record used for listing.
type Summary struct {
	ID           string
	Title        string
	MessageCount int
	UpdatedAt    string
}
