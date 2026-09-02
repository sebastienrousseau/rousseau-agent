package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// ssoBindingsSchema stores the (transport, external_id) →
// serialised sso.Identity mapping plus a wall-clock expiry so
// Lookup can filter stale rows without a background sweeper.
//
// The composite primary key (transport, external_id) mirrors the
// resolver key the callers use. The identity payload is stored
// as JSON so future Identity fields don't need a migration —
// same trade-off as the `sessions` table.
const ssoBindingsSchema = `
CREATE TABLE IF NOT EXISTS sso_bindings (
    transport    TEXT NOT NULL,
    external_id  TEXT NOT NULL,
    identity     TEXT NOT NULL,
    bound_at     TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    PRIMARY KEY (transport, external_id)
);
CREATE INDEX IF NOT EXISTS idx_sso_bindings_expires_at
    ON sso_bindings(expires_at);
`

// SSOBindings is a SQLite-backed [sso.BindingStore].
type SSOBindings struct {
	db *sql.DB
}

// NewSSOBindings returns an SSOBindings that shares the passed
// Store's SQLite database.
func NewSSOBindings(ctx context.Context, s *Store) (*SSOBindings, error) {
	if _, err := s.db.ExecContext(ctx, ssoBindingsSchema); err != nil {
		return nil, fmt.Errorf("sqlite: apply sso_bindings schema: %w", err)
	}
	return &SSOBindings{db: s.db}, nil
}

// Bind satisfies [sso.BindingStore]. Uses ON CONFLICT DO UPDATE
// so re-authenticating the same transport identifier just extends
// the binding's expiry — matches operator expectation ("scanning
// /login again shouldn't 'error already bound'").
func (b *SSOBindings) Bind(ctx context.Context, transport, externalID string, id sso.Identity, expiresAt time.Time) error {
	payload, err := json.Marshal(id)
	if err != nil {
		return fmt.Errorf("sqlite: marshal sso identity: %w", err)
	}
	const q = `
INSERT INTO sso_bindings (transport, external_id, identity, bound_at, expires_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(transport, external_id) DO UPDATE SET
    identity   = excluded.identity,
    bound_at   = excluded.bound_at,
    expires_at = excluded.expires_at
`
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	exp := expiresAt.UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := b.db.ExecContext(ctx, q, transport, externalID, string(payload), now, exp); err != nil {
		return fmt.Errorf("sqlite: bind sso identity: %w", err)
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
WHERE transport = ? AND external_id = ? AND expires_at > ?
`
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var payload string
	err := b.db.QueryRowContext(ctx, q, transport, externalID, now).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return sso.Identity{}, false, nil
	}
	if err != nil {
		return sso.Identity{}, false, fmt.Errorf("sqlite: lookup sso identity: %w", err)
	}
	var id sso.Identity
	if err := json.Unmarshal([]byte(payload), &id); err != nil {
		return sso.Identity{}, false, fmt.Errorf("sqlite: unmarshal sso identity: %w", err)
	}
	return id, true, nil
}

// Unbind satisfies [sso.BindingStore]. Idempotent.
func (b *SSOBindings) Unbind(ctx context.Context, transport, externalID string) error {
	const q = `DELETE FROM sso_bindings WHERE transport = ? AND external_id = ?`
	if _, err := b.db.ExecContext(ctx, q, transport, externalID); err != nil {
		return fmt.Errorf("sqlite: unbind sso identity: %w", err)
	}
	return nil
}

// Count satisfies [sso.BindingStore].
func (b *SSOBindings) Count(ctx context.Context) (int, error) {
	const q = `SELECT COUNT(*) FROM sso_bindings WHERE expires_at > ?`
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var n int
	if err := b.db.QueryRowContext(ctx, q, now).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlite: count sso bindings: %w", err)
	}
	return n, nil
}

// Compile-time interface satisfaction.
var _ sso.BindingStore = (*SSOBindings)(nil)
