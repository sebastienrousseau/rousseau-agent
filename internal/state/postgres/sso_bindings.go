package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// ssoBindingsSchema is the Postgres-flavoured mirror of the
// SQLite schema. Differences from the SQLite version and why:
//
//   - TIMESTAMPTZ for bound_at + expires_at instead of TEXT so
//     `expires_at > NOW()` filter uses index-native ordering.
//     SQLite formats time manually because it has no native
//     timestamp type; Postgres does.
//   - bound_at DEFAULT NOW() so the driver does not have to
//     format a wall-clock string on every Bind.
//   - identity JSONB instead of TEXT — same shape (JSON-serialised
//     [sso.Identity]) but the JSONB path validates + normalises at
//     insert time, catching a malformed payload immediately rather
//     than at the next Lookup unmarshal. Cost: one extra parse
//     on write, no downstream lookup cost.
//   - Composite PK stays (transport, external_id) — matches the
//     resolver key callers use.
//   - Same index on expires_at so the Lookup filter is cheap
//     regardless of how many stale rows sit on disk.
const ssoBindingsSchema = `
CREATE TABLE IF NOT EXISTS sso_bindings (
    transport   TEXT NOT NULL,
    external_id TEXT NOT NULL,
    identity    JSONB NOT NULL,
    bound_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (transport, external_id)
);
CREATE INDEX IF NOT EXISTS idx_sso_bindings_expires_at
    ON sso_bindings(expires_at);
`

// SSOBindings is a Postgres-backed [sso.BindingStore].
// Interface-compatible with [sqlite.SSOBindings] — the daemon can
// hold either concrete type via the [sso.BindingStore] interface
// without a shared factory.
type SSOBindings struct {
	db *sql.DB
}

// NewSSOBindings attaches to an existing Postgres [Store] and
// applies the schema. Idempotent — safe to call on every daemon
// boot.
func NewSSOBindings(ctx context.Context, s *Store) (*SSOBindings, error) {
	if _, err := s.db.ExecContext(ctx, ssoBindingsSchema); err != nil {
		return nil, fmt.Errorf("postgres: apply sso_bindings schema: %w", err)
	}
	return &SSOBindings{db: s.db}, nil
}

// Bind satisfies [sso.BindingStore]. ON CONFLICT DO UPDATE
// mirrors the SQLite driver's semantics: re-authenticating the
// same (transport, external_id) extends the binding's expiry —
// matches operator expectation ("scanning /login again shouldn't
// error 'already bound'"). bound_at is refreshed too so the row
// tells the truth about the last successful bind.
func (b *SSOBindings) Bind(ctx context.Context, transport, externalID string, id sso.Identity, expiresAt time.Time) error {
	payload, err := json.Marshal(id)
	if err != nil {
		return fmt.Errorf("postgres: marshal sso identity: %w", err)
	}
	const q = `
INSERT INTO sso_bindings (transport, external_id, identity, expires_at)
VALUES ($1, $2, $3::jsonb, $4)
ON CONFLICT (transport, external_id) DO UPDATE SET
    identity   = EXCLUDED.identity,
    bound_at   = NOW(),
    expires_at = EXCLUDED.expires_at
`
	if _, err := b.db.ExecContext(ctx, q, transport, externalID, string(payload), expiresAt.UTC()); err != nil {
		return fmt.Errorf("postgres: bind sso identity: %w", err)
	}
	return nil
}

// Lookup satisfies [sso.BindingStore]. Filters expired rows at
// SELECT time so callers never see stale identities. Expired
// rows stay on disk until Unbind or a follow-up sweep — the
// filter keeps them harmless in the meantime.
func (b *SSOBindings) Lookup(ctx context.Context, transport, externalID string) (sso.Identity, bool, error) {
	const q = `
SELECT identity FROM sso_bindings
WHERE transport = $1 AND external_id = $2 AND expires_at > NOW()
`
	var payload []byte
	err := b.db.QueryRowContext(ctx, q, transport, externalID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return sso.Identity{}, false, nil
	}
	if err != nil {
		return sso.Identity{}, false, fmt.Errorf("postgres: lookup sso identity: %w", err)
	}
	var id sso.Identity
	if err := json.Unmarshal(payload, &id); err != nil {
		return sso.Identity{}, false, fmt.Errorf("postgres: unmarshal sso identity: %w", err)
	}
	return id, true, nil
}

// Unbind satisfies [sso.BindingStore]. Idempotent.
func (b *SSOBindings) Unbind(ctx context.Context, transport, externalID string) error {
	const q = `DELETE FROM sso_bindings WHERE transport = $1 AND external_id = $2`
	if _, err := b.db.ExecContext(ctx, q, transport, externalID); err != nil {
		return fmt.Errorf("postgres: unbind sso identity: %w", err)
	}
	return nil
}

// Count satisfies [sso.BindingStore]. Excludes expired rows so
// the number matches "how many bindings are currently valid" —
// the metric operators actually care about.
func (b *SSOBindings) Count(ctx context.Context) (int, error) {
	const q = `SELECT COUNT(*) FROM sso_bindings WHERE expires_at > NOW()`
	var n int
	if err := b.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: count sso bindings: %w", err)
	}
	return n, nil
}

// Compile-time interface satisfaction — matches the SQLite
// driver's assertion so both concrete types can be handed to
// callers expecting [sso.BindingStore].
var _ sso.BindingStore = (*SSOBindings)(nil)
