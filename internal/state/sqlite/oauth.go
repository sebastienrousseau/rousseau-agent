package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// oauthTokensSchema is applied when an OAuthTokens is opened.
// Idempotent.
const oauthTokensSchema = `
CREATE TABLE IF NOT EXISTS oauth_tokens (
    provider    TEXT NOT NULL,
    account_id  TEXT NOT NULL,
    ciphertext  BLOB NOT NULL,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (provider, account_id)
);
`

// OAuthTokens persists provider+account → encrypted-token-blob rows.
// The plaintext token shape and encryption are the caller's
// responsibility; this store handles storage only.
type OAuthTokens struct{ db *sql.DB }

// NewOAuthTokens returns an OAuthTokens sharing the passed Store's
// SQLite database.
func NewOAuthTokens(ctx context.Context, s *Store) (*OAuthTokens, error) {
	if _, err := s.db.ExecContext(ctx, oauthTokensSchema); err != nil {
		return nil, fmt.Errorf("sqlite: apply oauth schema: %w", err)
	}
	return &OAuthTokens{db: s.db}, nil
}

// OAuthRow is a single decrypted row projection.
type OAuthRow struct {
	Provider   string
	AccountID  string
	Ciphertext []byte
	UpdatedAt  time.Time
}

// Put inserts or replaces the row for (provider, accountID). The
// ciphertext must already be sealed by the caller.
func (o *OAuthTokens) Put(ctx context.Context, provider, accountID string, ciphertext []byte) error {
	const q = `
INSERT INTO oauth_tokens (provider, account_id, ciphertext, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(provider, account_id) DO UPDATE SET
    ciphertext = excluded.ciphertext,
    updated_at = excluded.updated_at
`
	_, err := o.db.ExecContext(ctx, q, provider, accountID, ciphertext,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("sqlite: put oauth token: %w", err)
	}
	return nil
}

// Get returns the ciphertext for (provider, accountID) or ok=false if
// absent.
func (o *OAuthTokens) Get(ctx context.Context, provider, accountID string) ([]byte, bool, error) {
	const q = `SELECT ciphertext FROM oauth_tokens WHERE provider = ? AND account_id = ?`
	var ct []byte
	err := o.db.QueryRowContext(ctx, q, provider, accountID).Scan(&ct)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sqlite: get oauth token: %w", err)
	}
	return ct, true, nil
}

// Delete removes (provider, accountID). No error if the row does not
// exist.
func (o *OAuthTokens) Delete(ctx context.Context, provider, accountID string) error {
	_, err := o.db.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE provider = ? AND account_id = ?`,
		provider, accountID)
	if err != nil {
		return fmt.Errorf("sqlite: delete oauth token: %w", err)
	}
	return nil
}

// List returns every stored row's identifiers (provider, accountID).
// Ciphertext is not returned to keep this method safe for
// admin-listing use cases.
func (o *OAuthTokens) List(ctx context.Context) ([]OAuthRow, error) {
	const q = `SELECT provider, account_id, updated_at FROM oauth_tokens ORDER BY provider, account_id`
	rows, err := o.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list oauth tokens: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort close
	var out []OAuthRow
	for rows.Next() {
		var row OAuthRow
		var ts string
		if err := rows.Scan(&row.Provider, &row.AccountID, &ts); err != nil {
			return nil, fmt.Errorf("sqlite: scan oauth row: %w", err)
		}
		row.UpdatedAt, _ = time.Parse(time.RFC3339Nano, ts) //nolint:errcheck // best-effort parse; zero on failure
		out = append(out, row)
	}
	return out, rows.Err()
}

// Iterate walks every row invoking fn with the raw ciphertext. Used
// by the rotate-key admin path so it can re-seal every row under a
// new master key without deserialising through Go objects.
func (o *OAuthTokens) Iterate(ctx context.Context, fn func(provider, accountID string, ciphertext []byte) error) error {
	const q = `SELECT provider, account_id, ciphertext FROM oauth_tokens`
	rows, err := o.db.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("sqlite: iterate oauth: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort close
	for rows.Next() {
		var (
			provider, account string
			ct                []byte
		)
		if err := rows.Scan(&provider, &account, &ct); err != nil {
			return fmt.Errorf("sqlite: scan oauth row: %w", err)
		}
		if err := fn(provider, account, ct); err != nil {
			return err
		}
	}
	return rows.Err()
}
