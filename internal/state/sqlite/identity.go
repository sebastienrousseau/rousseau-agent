package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/identity"
)

const identitySchema = `
CREATE TABLE IF NOT EXISTS identities (
    id              TEXT PRIMARY KEY,
    primary_display TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS identity_handles (
    transport   TEXT NOT NULL,
    sender      TEXT NOT NULL,
    identity_id TEXT NOT NULL,
    verified_at TEXT NOT NULL,
    PRIMARY KEY (transport, sender),
    FOREIGN KEY (identity_id) REFERENCES identities(id)
);

CREATE INDEX IF NOT EXISTS idx_identity_handles_identity
    ON identity_handles(identity_id);
`

// IdentityStore is the SQLite-backed [identity.Resolver] implementation.
type IdentityStore struct {
	db *sql.DB
}

// NewIdentityStore applies the schema and returns a resolver that
// shares s's underlying *sql.DB. Idempotent.
func NewIdentityStore(ctx context.Context, s *Store) (*IdentityStore, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sqlite: nil store")
	}
	if _, err := s.db.ExecContext(ctx, identitySchema); err != nil {
		return nil, fmt.Errorf("sqlite: apply identity schema: %w", err)
	}
	return &IdentityStore{db: s.db}, nil
}

// Resolve satisfies [identity.Resolver].
func (r *IdentityStore) Resolve(ctx context.Context, transport, sender string) (identity.ID, error) {
	const q = `SELECT identity_id FROM identity_handles WHERE transport=? AND sender=?`
	var id string
	err := r.db.QueryRowContext(ctx, q, transport, sender).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", identity.ErrNotLinked
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: resolve: %w", err)
	}
	return identity.ID(id), nil
}

// Provision satisfies [identity.Resolver]. Idempotent — repeated
// calls on the same (transport, sender) return the same ID.
func (r *IdentityStore) Provision(ctx context.Context, transport, sender, display string) (identity.ID, error) {
	if existing, err := r.Resolve(ctx, transport, sender); err == nil {
		return existing, nil
	}
	id := newIdentityID()
	now := utcRFC3339(time.Now())

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("sqlite: provision: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // best-effort cleanup

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO identities (id, primary_display, created_at) VALUES (?, ?, ?)`,
		id, display, now,
	); err != nil {
		return "", fmt.Errorf("sqlite: provision: insert identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO identity_handles (transport, sender, identity_id, verified_at) VALUES (?, ?, ?, ?)`,
		transport, sender, id, now,
	); err != nil {
		return "", fmt.Errorf("sqlite: provision: insert handle: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("sqlite: provision: commit: %w", err)
	}
	return identity.ID(id), nil
}

// Link satisfies [identity.Resolver].
func (r *IdentityStore) Link(ctx context.Context, id identity.ID, transport, sender string) error {
	// Check the identity exists AND the handle isn't already bound.
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	if existing, err := r.Resolve(ctx, transport, sender); err == nil {
		if existing == id {
			return nil // idempotent: already linked to us
		}
		return identity.ErrAlreadyLinked
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO identity_handles (transport, sender, identity_id, verified_at) VALUES (?, ?, ?, ?)`,
		transport, sender, string(id), utcRFC3339(time.Now()),
	)
	if err != nil {
		return fmt.Errorf("sqlite: link: %w", err)
	}
	return nil
}

// Unlink satisfies [identity.Resolver].
func (r *IdentityStore) Unlink(ctx context.Context, transport, sender string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM identity_handles WHERE transport=? AND sender=?`,
		transport, sender,
	)
	if err != nil {
		return fmt.Errorf("sqlite: unlink: %w", err)
	}
	n, _ := res.RowsAffected() //nolint:errcheck // sqlite RowsAffected can't fail here
	if n == 0 {
		return identity.ErrNotLinked
	}
	return nil
}

// Get satisfies [identity.Resolver].
func (r *IdentityStore) Get(ctx context.Context, id identity.ID) (identity.Identity, error) {
	const q = `SELECT primary_display, created_at FROM identities WHERE id=?`
	var (
		display string
		created string
	)
	err := r.db.QueryRowContext(ctx, q, string(id)).Scan(&display, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Identity{}, identity.ErrIdentityNotFound
	}
	if err != nil {
		return identity.Identity{}, fmt.Errorf("sqlite: get identity: %w", err)
	}
	createdAt, _ := time.Parse("2006-01-02T15:04:05.000Z", created) //nolint:errcheck // malformed timestamp → zero value is fine for display

	handles, err := r.HandlesFor(ctx, id)
	if err != nil {
		return identity.Identity{}, err
	}
	return identity.Identity{
		ID:             id,
		PrimaryDisplay: display,
		Handles:        handles,
		CreatedAt:      createdAt,
	}, nil
}

// HandlesFor satisfies [identity.Resolver].
func (r *IdentityStore) HandlesFor(ctx context.Context, id identity.ID) ([]identity.Handle, error) {
	const q = `SELECT transport, sender, verified_at FROM identity_handles WHERE identity_id=? ORDER BY verified_at ASC`
	rows, err := r.db.QueryContext(ctx, q, string(id))
	if err != nil {
		return nil, fmt.Errorf("sqlite: handles for: %w", err)
	}
	defer rows.Close() //nolint:errcheck // best-effort cleanup

	var out []identity.Handle
	for rows.Next() {
		var h identity.Handle
		var verified string
		if err := rows.Scan(&h.Transport, &h.Sender, &verified); err != nil {
			return nil, fmt.Errorf("sqlite: scan handle: %w", err)
		}
		h.VerifiedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", verified) //nolint:errcheck // malformed timestamp → zero value is fine for display
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate handles: %w", err)
	}
	return out, nil
}

// -- helpers -----------------------------------------------------------

func newIdentityID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read can't realistically fail on Linux; if it
		// does the daemon can't function anyway.
		panic("identity: rand.Read: " + err.Error()) //nolint:forbidigo // documented panic on unrecoverable init failure
	}
	return "id-" + hex.EncodeToString(b[:])
}

func utcRFC3339(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// Compile-time interface satisfaction.
var _ identity.Resolver = (*IdentityStore)(nil)
