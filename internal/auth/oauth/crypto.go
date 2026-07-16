// Package oauth implements the OAuth2 broker and encrypted token
// store used by the native tool integrations (Google Workspace,
// GitHub, Slack, Linear, Stripe).
//
// Tokens at rest are AEAD-encrypted with XChaCha20-Poly1305; the
// master key is resolved from the ROUSSEAU_TOKEN_KEY environment
// variable first, then from an OS keyring, then from a
// chmod-0600 file under the XDG state dir as a last resort.
package oauth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the AEAD key size in bytes.
const KeySize = chacha20poly1305.KeySize // 32

// nonceSize is XChaCha20-Poly1305's 24-byte nonce.
const nonceSize = chacha20poly1305.NonceSizeX

// Cipher wraps an AEAD-ready master key and provides the two
// Seal / Open operations the token store needs.
type Cipher struct {
	aead interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
		NonceSize() int
		Overhead() int
	}
}

// NewCipher constructs a Cipher from a 32-byte master key. Passing a
// wrong-length key returns an error rather than panicking so callers
// that source keys from env / files can report a clean message.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("oauth: master key must be %d bytes, got %d", KeySize, len(key))
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("oauth: init aead: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Seal produces a nonce-prefixed ciphertext. additionalData is bound
// to the ciphertext via the AEAD; the exact same additionalData must
// be passed to Open to succeed.
//
// Callers should use a stable, non-secret AAD (e.g. the provider name
// and account id) so a swapped ciphertext from a different row is
// detected on decrypt.
func (c *Cipher) Seal(plaintext, additionalData []byte) ([]byte, error) {
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("oauth: read nonce: %w", err)
	}
	ct := c.aead.Seal(nil, nonce, plaintext, additionalData)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Open reverses [Cipher.Seal]. additionalData must match what was
// passed to Seal.
func (c *Cipher) Open(sealed, additionalData []byte) ([]byte, error) {
	if len(sealed) < nonceSize+c.aead.Overhead() {
		return nil, fmt.Errorf("oauth: ciphertext too short")
	}
	nonce := sealed[:nonceSize]
	ct := sealed[nonceSize:]
	pt, err := c.aead.Open(nil, nonce, ct, additionalData)
	if err != nil {
		return nil, fmt.Errorf("oauth: open: %w", err)
	}
	return pt, nil
}

// GenerateKey returns a fresh 32-byte random key. Callers should hex-
// encode the result for env-var transport; see [DecodeHexKey].
func GenerateKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return nil, fmt.Errorf("oauth: generate key: %w", err)
	}
	return k, nil
}

// DecodeHexKey parses a 64-character hex string into a raw key. Empty
// input returns (nil, nil) so callers can fall through to other key
// sources on empty ROUSSEAU_TOKEN_KEY.
func DecodeHexKey(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	if len(s) != 2*KeySize {
		return nil, fmt.Errorf("oauth: hex key must be %d chars, got %d", 2*KeySize, len(s))
	}
	return hex.DecodeString(s)
}

// EncodeHexKey is the inverse of [DecodeHexKey].
func EncodeHexKey(key []byte) string { return hex.EncodeToString(key) }
