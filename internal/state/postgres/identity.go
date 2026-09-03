package postgres

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

// identitySchema is the Postgres-flavoured mirror of the SQLite
// schema. Differences from the SQLite version and why:
//
//   - TIMESTAMPTZ for created_at + verified_at instead of TEXT
//     so range queries + ORDER BY use native indexes; matches
//     the pattern established by the other §2.4a ports.
//   - DEFAULT NOW() on both timestamp columns so the driver
//     doesn't have to format wall-clock strings on every write.
//   - FOREIGN KEY with ON DELETE CASCADE — deleting an identity
//     should cascade to its handles. The SQLite driver relies on
//     `foreign_keys = ON` at connection time; Postgres enforces
//     FK integrity unconditionally, so cascading is the safer
//     default (matches operator expectation, prevents dangling
//     handles that a follow-up Lookup would falsely resolve).
//   - Same composite PK (transport, sender) on identity_handles
//     and same identity_id index — the query shapes are the
//     same across drivers.
const identitySchema = `
CREATE TABLE IF NOT EXISTS identities (
    id              TEXT PRIMARY KEY,
    primary_display TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS identity_handles (
    transport   TEXT NOT NULL,
    sender      TEXT NOT NULL,
    identity_id TEXT NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    verified_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (transport, sender)
);

CREATE INDEX IF NOT EXISTS idx_identity_handles_identity
    ON identity_handles(identity_id);
`

// IdentityStore is the Postgres-backed [identity.Resolver]
// implementation. Interface-compatible with [sqlite.IdentityStore]
// via the shared identity.Resolver interface.
type IdentityStore struct {
	db *sql.DB
}

// NewIdentityStore attaches to an existing Postgres [Store] and
// applies the schema. Idempotent — safe to call on every daemon
// boot.
func NewIdentityStore(ctx context.Context, s *Store) (*IdentityStore, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("postgres: nil store")
	}
	if _, err := s.db.ExecContext(ctx, identitySchema); err != nil {
		return nil, fmt.Errorf("postgres: apply identity schema: %w", err)
	}
	return &IdentityStore{db: s.db}, nil
}

// Resolve satisfies [identity.Resolver]. Returns ErrNotLinked on
// missing handle so callers can distinguish "unknown sender" from
// a real error and auto-provision.
func (r *IdentityStore) Resolve(ctx context.Context, transport, sender string) (identity.ID, error) {
	const q = `SELECT identity_id FROM identity_handles WHERE transport=$1 AND sender=$2`
	var id string
	err := r.db.QueryRowContext(ctx, q, transport, sender).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", identity.ErrNotLinked
	}
	if err != nil {
		return "", fmt.Errorf("postgres: resolve: %w", err)
	}
	return identity.ID(id), nil
}

// Provision satisfies [identity.Resolver]. Idempotent — repeated
// calls on the same (transport, sender) return the same ID. The
// two INSERTs run in a transaction so a partial failure never
// leaves an identity row without its bootstrapping handle.
func (r *IdentityStore) Provision(ctx context.Context, transport, sender, display string) (identity.ID, error) {
	if existing, err := r.Resolve(ctx, transport, sender); err == nil {
		return existing, nil
	}
	id := newIdentityID()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("postgres: provision: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // best-effort cleanup

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO identities (id, primary_display) VALUES ($1, $2)`,
		id, display,
	); err != nil {
		return "", fmt.Errorf("postgres: provision: insert identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO identity_handles (transport, sender, identity_id) VALUES ($1, $2, $3)`,
		transport, sender, id,
	); err != nil {
		return "", fmt.Errorf("postgres: provision: insert handle: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("postgres: provision: commit: %w", err)
	}
	return identity.ID(id), nil
}

// Link satisfies [identity.Resolver]. Enforces two invariants:
// the target identity must exist (else ErrIdentityNotFound), and
// the handle must not already be linked to a different identity
// (else ErrAlreadyLinked). Idempotent when the handle is already
// linked to this same identity.
func (r *IdentityStore) Link(ctx context.Context, id identity.ID, transport, sender string) error {
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	if existing, err := r.Resolve(ctx, transport, sender); err == nil {
		if existing == id {
			return nil
		}
		return identity.ErrAlreadyLinked
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO identity_handles (transport, sender, identity_id) VALUES ($1, $2, $3)`,
		transport, sender, string(id),
	)
	if err != nil {
		return fmt.Errorf("postgres: link: %w", err)
	}
	return nil
}

// Unlink satisfies [identity.Resolver]. Returns ErrNotLinked when
// the handle is not present — matches the SQLite driver so
// operator scripts get the same error surface.
func (r *IdentityStore) Unlink(ctx context.Context, transport, sender string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM identity_handles WHERE transport=$1 AND sender=$2`,
		transport, sender,
	)
	if err != nil {
		return fmt.Errorf("postgres: unlink: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres: unlink rows-affected: %w", err)
	}
	if n == 0 {
		return identity.ErrNotLinked
	}
	return nil
}

// Get satisfies [identity.Resolver].
func (r *IdentityStore) Get(ctx context.Context, id identity.ID) (identity.Identity, error) {
	const q = `SELECT primary_display, created_at FROM identities WHERE id=$1`
	var (
		display string
		created time.Time
	)
	err := r.db.QueryRowContext(ctx, q, string(id)).Scan(&display, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Identity{}, identity.ErrIdentityNotFound
	}
	if err != nil {
		return identity.Identity{}, fmt.Errorf("postgres: get identity: %w", err)
	}

	handles, err := r.HandlesFor(ctx, id)
	if err != nil {
		return identity.Identity{}, err
	}
	return identity.Identity{
		ID:             id,
		PrimaryDisplay: display,
		Handles:        handles,
		CreatedAt:      created.UTC(),
	}, nil
}

// HandlesFor satisfies [identity.Resolver]. Ordered by
// verified_at ASC so the transport that first bound the identity
// renders first — matches the SQLite driver so /whoami output is
// deterministic across drivers.
func (r *IdentityStore) HandlesFor(ctx context.Context, id identity.ID) ([]identity.Handle, error) {
	const q = `SELECT transport, sender, verified_at FROM identity_handles WHERE identity_id=$1 ORDER BY verified_at ASC`
	rows, err := r.db.QueryContext(ctx, q, string(id))
	if err != nil {
		return nil, fmt.Errorf("postgres: handles for: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []identity.Handle
	for rows.Next() {
		var (
			h        identity.Handle
			verified time.Time
		)
		if err := rows.Scan(&h.Transport, &h.Sender, &verified); err != nil {
			return nil, fmt.Errorf("postgres: scan handle: %w", err)
		}
		h.VerifiedAt = verified.UTC()
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate handles: %w", err)
	}
	return out, nil
}

// -- helpers -----------------------------------------------------------

// newIdentityID returns a 16-byte random id prefixed with "id-".
// Same shape as the SQLite driver so identity IDs are visually
// indistinguishable regardless of backend.
func newIdentityID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read can't realistically fail on Linux; if
		// it does the daemon can't function anyway.
		panic("identity: rand.Read: " + err.Error()) //nolint:forbidigo // documented panic on unrecoverable init failure
	}
	return "id-" + hex.EncodeToString(b[:])
}

// Compile-time interface satisfaction — matches the SQLite
// driver's assertion so both concrete types can be handed to
// callers expecting [identity.Resolver].
var _ identity.Resolver = (*IdentityStore)(nil)
