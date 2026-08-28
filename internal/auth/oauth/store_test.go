package oauth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memStore is a TokenStore for tests.
type memStore struct {
	rows map[string][]byte
}

func newMemStore() *memStore { return &memStore{rows: map[string][]byte{}} }

func key(p, a string) string { return p + "|" + a }

func (m *memStore) Put(_ context.Context, provider, accountID string, ct []byte) error {
	m.rows[key(provider, accountID)] = ct
	return nil
}
func (m *memStore) Get(_ context.Context, provider, accountID string) ([]byte, bool, error) {
	ct, ok := m.rows[key(provider, accountID)]
	return ct, ok, nil
}
func (m *memStore) Delete(_ context.Context, provider, accountID string) error {
	delete(m.rows, key(provider, accountID))
	return nil
}
func (m *memStore) List(_ context.Context) ([]StoredRow, error) {
	out := make([]StoredRow, 0, len(m.rows))
	for k := range m.rows {
		out = append(out, StoredRow{
			Provider:  k[:len(k)-len("|default")],
			AccountID: "default",
		})
	}
	return out, nil
}
func (m *memStore) Iterate(_ context.Context, fn func(provider, accountID string, ct []byte) error) error {
	for k, ct := range m.rows {
		provider := ""
		account := ""
		for i, r := range k {
			if r == '|' {
				provider = k[:i]
				account = k[i+1:]
				break
			}
		}
		if err := fn(provider, account, ct); err != nil {
			return err
		}
	}
	return nil
}

func newVault(t *testing.T) (*Vault, *Cipher) {
	t.Helper()
	k, err := GenerateKey()
	require.NoError(t, err)
	c, err := NewCipher(k)
	require.NoError(t, err)
	return NewVault(newMemStore(), c), c
}

func TestVault_PutGetRoundTrip(t *testing.T) {
	v, _ := newVault(t)
	tok := &Token{AccessToken: "at", RefreshToken: "rt", Expiry: time.Now().Add(time.Hour)}
	require.NoError(t, v.Put(context.Background(), "google", "default", tok))

	got, ok, err := v.Get(context.Background(), "google", "default")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, tok.AccessToken, got.AccessToken)
	assert.Equal(t, tok.RefreshToken, got.RefreshToken)
}

func TestVault_MissingIsNotFound(t *testing.T) {
	v, _ := newVault(t)
	_, ok, err := v.Get(context.Background(), "google", "nobody")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestVault_DeleteRemoves(t *testing.T) {
	v, _ := newVault(t)
	require.NoError(t, v.Put(context.Background(), "google", "default", &Token{AccessToken: "at"}))
	require.NoError(t, v.Delete(context.Background(), "google", "default"))
	_, ok, err := v.Get(context.Background(), "google", "default")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestVault_PutNilRejected(t *testing.T) {
	v, _ := newVault(t)
	assert.Error(t, v.Put(context.Background(), "google", "default", nil))
}

func TestVault_RotateKeyPreservesPlaintext(t *testing.T) {
	v, _ := newVault(t)
	tokens := map[string]*Token{
		"google:default": {AccessToken: "g-at", RefreshToken: "g-rt"},
		"github:default": {AccessToken: "gh-at"},
	}
	for k, tok := range tokens {
		provider := ""
		account := ""
		for i, r := range k {
			if r == ':' {
				provider = k[:i]
				account = k[i+1:]
				break
			}
		}
		require.NoError(t, v.Put(context.Background(), provider, account, tok))
	}

	newKey, err := GenerateKey()
	require.NoError(t, err)
	newCipher, err := NewCipher(newKey)
	require.NoError(t, err)

	require.NoError(t, v.RotateKey(context.Background(), newCipher))
	v.SetCipher(newCipher)

	// All rows are now readable under the new cipher.
	for k, want := range tokens {
		provider := ""
		account := ""
		for i, r := range k {
			if r == ':' {
				provider = k[:i]
				account = k[i+1:]
				break
			}
		}
		got, ok, err := v.Get(context.Background(), provider, account)
		require.NoError(t, err, k)
		require.True(t, ok, k)
		assert.Equal(t, want.AccessToken, got.AccessToken, k)
	}
}

func TestVault_TamperedRowFailsToOpen(t *testing.T) {
	v, _ := newVault(t)
	require.NoError(t, v.Put(context.Background(), "google", "default", &Token{AccessToken: "at"}))
	// Reach into the underlying store and flip a byte.
	ms := v.store.(*memStore)
	ct := ms.rows[key("google", "default")]
	ct[len(ct)-1] ^= 0x01
	_, _, err := v.Get(context.Background(), "google", "default")
	assert.Error(t, err)
}
