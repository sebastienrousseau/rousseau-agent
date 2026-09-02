package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	sqliteoauth "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// oauthTokensSchema is the Postgres-flavoured mirror of the SQLite
// schema. Differences from the SQLite version and why:
//
//   - `ciphertext BYTEA` instead of BLOB — Postgres's native
//     binary type. The lib/pq legacy escaping tax does not apply
//     to pgx/stdlib.
//   - `updated_at TIMESTAMPTZ DEFAULT NOW()` instead of TEXT — the
//     SQLite driver formats time manually because SQLite has no
//     native timestamp type; Postgres does, so let the DB stamp it.
//     Removes a formatting round trip from Put.
//   - Composite PK stays (provider, account_id) — the operator-
//     visible identity of an OAuth token is the pair, not either
//     side alone.
const oauthTokensSchema = `
CREATE TABLE IF NOT EXISTS oauth_tokens (
    provider   TEXT NOT NULL,
    account_id TEXT NOT NULL,
    ciphertext BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (provider, account_id)
);
`

// OAuthRow is the domain type. Re-exported from
// internal/state/sqlite so callers can accept either driver's
// OAuthTokens without type-swapping.
type OAuthRow = sqliteoauth.OAuthRow

// OAuthTokens is a Postgres-backed provider+account →
// encrypted-token-blob store. The plaintext token shape and
// encryption are the caller's responsibility; this store handles
// storage only.
//
// Interface-compatible with [sqliteoauth.OAuthTokens] — matching
// signatures let the daemon hold either concrete type without a
// shared interface (which would just re-declare what the two
// mirror).
type OAuthTokens struct{ db *sql.DB }

// NewOAuthTokens attaches to an existing Postgres [Store] and
// applies the schema. Idempotent — safe to call on every daemon
// boot.
func NewOAuthTokens(ctx context.Context, s *Store) (*OAuthTokens, error) {
	if _, err := s.db.ExecContext(ctx, oauthTokensSchema); err != nil {
		return nil, fmt.Errorf("postgres: apply oauth schema: %w", err)
	}
	return &OAuthTokens{db: s.db}, nil
}

// Put inserts or replaces the row for (provider, accountID). The
// ciphertext must already be sealed by the caller. ON CONFLICT DO
// UPDATE mirrors the SQLite driver's replace-on-conflict semantics
// so key rotation and token refresh paths behave identically
// across drivers.
func (o *OAuthTokens) Put(ctx context.Context, provider, accountID string, ciphertext []byte) error {
	const q = `
INSERT INTO oauth_tokens (provider, account_id, ciphertext, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (provider, account_id) DO UPDATE SET
    ciphertext = EXCLUDED.ciphertext,
    updated_at = EXCLUDED.updated_at
`
	if _, err := o.db.ExecContext(ctx, q, provider, accountID, ciphertext); err != nil {
		return fmt.Errorf("postgres: put oauth token: %w", err)
	}
	return nil
}

// Get returns the ciphertext for (provider, accountID) or ok=false
// if absent.
func (o *OAuthTokens) Get(ctx context.Context, provider, accountID string) ([]byte, bool, error) {
	const q = `SELECT ciphertext FROM oauth_tokens WHERE provider = $1 AND account_id = $2`
	var ct []byte
	err := o.db.QueryRowContext(ctx, q, provider, accountID).Scan(&ct)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("postgres: get oauth token: %w", err)
	}
	return ct, true, nil
}

// Delete removes (provider, accountID). No error if the row does
// not exist — matches the SQLite driver so idempotent cleanup
// scripts do not need per-driver branches.
func (o *OAuthTokens) Delete(ctx context.Context, provider, accountID string) error {
	_, err := o.db.ExecContext(ctx,
		`DELETE FROM oauth_tokens WHERE provider = $1 AND account_id = $2`,
		provider, accountID)
	if err != nil {
		return fmt.Errorf("postgres: delete oauth token: %w", err)
	}
	return nil
}

// List returns every stored row's identifiers (provider,
// accountID) plus updated_at. Ciphertext is not returned to keep
// this method safe for admin-listing use cases.
func (o *OAuthTokens) List(ctx context.Context) ([]OAuthRow, error) {
	const q = `SELECT provider, account_id, updated_at FROM oauth_tokens ORDER BY provider, account_id`
	rows, err := o.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("postgres: list oauth tokens: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort close
	var out []OAuthRow
	for rows.Next() {
		var row OAuthRow
		if err := rows.Scan(&row.Provider, &row.AccountID, &row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan oauth row: %w", err)
		}
		row.UpdatedAt = row.UpdatedAt.UTC()
		out = append(out, row)
	}
	return out, rows.Err()
}

// Iterate walks every row invoking fn with the raw ciphertext.
// Used by the rotate-key admin path so it can re-seal every row
// under a new master key without deserialising through Go objects.
func (o *OAuthTokens) Iterate(ctx context.Context, fn func(provider, accountID string, ciphertext []byte) error) error {
	const q = `SELECT provider, account_id, ciphertext FROM oauth_tokens`
	rows, err := o.db.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("postgres: iterate oauth: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort close
	for rows.Next() {
		var (
			provider, account string
			ct                []byte
		)
		if err := rows.Scan(&provider, &account, &ct); err != nil {
			return fmt.Errorf("postgres: scan oauth row: %w", err)
		}
		if err := fn(provider, account, ct); err != nil {
			return err
		}
	}
	return rows.Err()
}
