package sso

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exp and iss are mandatory: a token the IdP signed but that carries
// neither used to be accepted and bound for 24 hours.
func TestOIDC_RejectsTokensMissingExpOrIss(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	noExp := s.signToken(t, map[string]any{"iss": s.url, "sub": "u1"})
	_, err = d.VerifyToken(context.Background(), noExp)
	require.ErrorIs(t, err, ErrTokenInvalid)
	assert.Contains(t, err.Error(), "exp")

	noIss := s.signToken(t, map[string]any{"sub": "u1", "exp": time.Now().Add(time.Hour).Unix()})
	_, err = d.VerifyToken(context.Background(), noIss)
	require.ErrorIs(t, err, ErrIssuerMismatch)
	assert.Contains(t, err.Error(), "iss")
}

// A second unknown-kid refresh inside jwksMinRefresh is refused so
// /login spam cannot turn every attempt into an IdP round-trip.
func TestOIDC_UnknownKidRefreshIsRateLimited(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url, JWKSRefresh: time.Hour}, silentLogger())
	require.NoError(t, err)

	claims := map[string]any{"iss": s.url, "sub": "u1", "exp": time.Now().Add(time.Hour).Unix()}
	// Warm the cache: the cold fetch is a first fetch, not a forced one.
	_, err = d.VerifyToken(context.Background(), s.signToken(t, claims))
	require.NoError(t, err)
	// First unknown kid forces one refresh.
	_, err = d.VerifyToken(context.Background(), signRS256Token(t, s.priv, "rotated-1", claims))
	require.Error(t, err)
	afterFirst := s.jwksHits.Load()
	assert.Equal(t, int64(2), afterFirst)

	_, err = d.VerifyToken(context.Background(), signRS256Token(t, s.priv, "rotated-2", claims))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate-limited")
	assert.Equal(t, afterFirst, s.jwksHits.Load(), "no second JWKS fetch inside the window")

	// Known kids keep working from the cache meanwhile.
	_, err = d.VerifyToken(context.Background(), s.signToken(t, claims))
	assert.NoError(t, err)
}
