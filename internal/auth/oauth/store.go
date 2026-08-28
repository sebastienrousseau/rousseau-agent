package oauth

import (
	"context"
	"encoding/json"
	"fmt"
)

// TokenStore persists encrypted [Token] blobs. Implementations back
// onto SQLite in production and an in-memory map in tests.
type TokenStore interface {
	Put(ctx context.Context, provider, accountID string, ciphertext []byte) error
	Get(ctx context.Context, provider, accountID string) ([]byte, bool, error)
	Delete(ctx context.Context, provider, accountID string) error
	List(ctx context.Context) ([]StoredRow, error)
	Iterate(ctx context.Context, fn func(provider, accountID string, ciphertext []byte) error) error
}

// StoredRow is the projection returned by [TokenStore.List].
type StoredRow struct {
	Provider  string
	AccountID string
}

// Vault couples a [TokenStore] with a [Cipher] to expose Put/Get in
// terms of the plaintext [Token] callers care about.
type Vault struct {
	store  TokenStore
	cipher *Cipher
}

// NewVault wires a store and cipher into a Vault. Both are required.
func NewVault(store TokenStore, cipher *Cipher) *Vault {
	return &Vault{store: store, cipher: cipher}
}

// Put encrypts tok and writes it under (provider, accountID). The
// authenticated-data field is bound to "provider:accountID" so
// swapping a ciphertext into a different row is detected on decrypt.
func (v *Vault) Put(ctx context.Context, provider, accountID string, tok *Token) error {
	if tok == nil {
		return fmt.Errorf("oauth: nil token")
	}
	pt, err := json.Marshal(tok)
	if err != nil {
		return fmt.Errorf("oauth: marshal token: %w", err)
	}
	ct, err := v.cipher.Seal(pt, aad(provider, accountID))
	if err != nil {
		return err
	}
	return v.store.Put(ctx, provider, accountID, ct)
}

// Get reads and decrypts the row under (provider, accountID). ok is
// false when no row exists; other errors surface as-is.
func (v *Vault) Get(ctx context.Context, provider, accountID string) (*Token, bool, error) {
	ct, ok, err := v.store.Get(ctx, provider, accountID)
	if err != nil || !ok {
		return nil, ok, err
	}
	pt, err := v.cipher.Open(ct, aad(provider, accountID))
	if err != nil {
		return nil, true, err
	}
	var tok Token
	if err := json.Unmarshal(pt, &tok); err != nil {
		return nil, true, fmt.Errorf("oauth: unmarshal token: %w", err)
	}
	return &tok, true, nil
}

// Delete removes (provider, accountID).
func (v *Vault) Delete(ctx context.Context, provider, accountID string) error {
	return v.store.Delete(ctx, provider, accountID)
}

// List enumerates every stored (provider, accountID) tuple.
func (v *Vault) List(ctx context.Context) ([]StoredRow, error) {
	return v.store.List(ctx)
}

// RotateKey re-encrypts every row under the new cipher. The old
// cipher must still be able to decrypt the current rows. On error
// the operation stops at the failing row; already-rotated rows stay
// rotated. Callers should re-run RotateKey after fixing the failure.
func (v *Vault) RotateKey(ctx context.Context, newCipher *Cipher) error {
	return v.store.Iterate(ctx, func(provider, accountID string, ct []byte) error {
		pt, err := v.cipher.Open(ct, aad(provider, accountID))
		if err != nil {
			return fmt.Errorf("oauth: rotate: open %s/%s: %w", provider, accountID, err)
		}
		reSealed, err := newCipher.Seal(pt, aad(provider, accountID))
		if err != nil {
			return err
		}
		return v.store.Put(ctx, provider, accountID, reSealed)
	})
}

// SetCipher swaps the vault's cipher (after RotateKey has migrated
// every row).
func (v *Vault) SetCipher(c *Cipher) { v.cipher = c }

// aad binds a ciphertext to the row's identity.
func aad(provider, accountID string) []byte {
	return []byte(provider + ":" + accountID)
}
