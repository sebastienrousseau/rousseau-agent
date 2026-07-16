package oauth

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCipher(t *testing.T) *Cipher {
	t.Helper()
	k, err := GenerateKey()
	require.NoError(t, err)
	c, err := NewCipher(k)
	require.NoError(t, err)
	return c
}

func TestCipher_RoundTrip(t *testing.T) {
	c := newCipher(t)
	pt := []byte(`{"access_token":"sk-secret","refresh_token":"rf-secret"}`)
	aad := []byte("google:alice@example.com")

	ct, err := c.Seal(pt, aad)
	require.NoError(t, err)
	assert.NotContains(t, string(ct), "sk-secret")

	got, err := c.Open(ct, aad)
	require.NoError(t, err)
	assert.Equal(t, pt, got)
}

func TestCipher_TamperedAADFails(t *testing.T) {
	c := newCipher(t)
	ct, err := c.Seal([]byte("hello"), []byte("aad-a"))
	require.NoError(t, err)
	_, err = c.Open(ct, []byte("aad-b"))
	assert.Error(t, err, "swapped AAD must not decrypt")
}

func TestCipher_TamperedCiphertextFails(t *testing.T) {
	c := newCipher(t)
	ct, err := c.Seal([]byte("hello"), nil)
	require.NoError(t, err)
	// Flip a byte in the tag area.
	ct[len(ct)-1] ^= 0x01
	_, err = c.Open(ct, nil)
	assert.Error(t, err)
}

func TestCipher_NonceUniqueness(t *testing.T) {
	c := newCipher(t)
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		ct, err := c.Seal([]byte("x"), nil)
		require.NoError(t, err)
		nonce := string(ct[:nonceSize])
		require.False(t, seen[nonce], "nonce reuse detected on iter %d", i)
		seen[nonce] = true
	}
}

func TestNewCipher_RejectsWrongLength(t *testing.T) {
	_, err := NewCipher(make([]byte, 16))
	assert.Error(t, err)
	_, err = NewCipher(make([]byte, 64))
	assert.Error(t, err)
}

func TestCipher_OpenRejectsShort(t *testing.T) {
	c := newCipher(t)
	_, err := c.Open([]byte{0, 1, 2}, nil)
	assert.Error(t, err)
}

func TestDecodeHexKey_EmptyIsNil(t *testing.T) {
	k, err := DecodeHexKey("")
	require.NoError(t, err)
	assert.Nil(t, k)
}

func TestDecodeHexKey_RejectsBadLength(t *testing.T) {
	_, err := DecodeHexKey("cafe")
	assert.Error(t, err)
}

func TestDecodeHexKey_RoundTripsEncode(t *testing.T) {
	raw := make([]byte, KeySize)
	_, _ = rand.Read(raw)
	encoded := EncodeHexKey(raw)
	decoded, err := DecodeHexKey(encoded)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(raw, decoded))
}

func TestGenerateKey_DifferentEachCall(t *testing.T) {
	a, err := GenerateKey()
	require.NoError(t, err)
	b, err := GenerateKey()
	require.NoError(t, err)
	assert.False(t, bytes.Equal(a, b))
	assert.Len(t, a, KeySize)
}
