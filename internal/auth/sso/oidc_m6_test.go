package sso

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOIDC_EmptyAudienceIsConfigError(t *testing.T) {
	_, err := NewOIDCDirectory(OIDCConfig{Issuer: "https://idp.example"}, silentLogger())
	require.Error(t, err, "without an audience, tokens for any client of the issuer would be accepted")
	assert.Contains(t, err.Error(), "audience")

	_, err = NewOIDCDirectory(OIDCConfig{Issuer: "https://idp.example", AllowAnyAudience: true}, silentLogger())
	require.NoError(t, err, "allow_any_audience is the explicit opt-out")
}

func TestOIDC_TokenIDFromJTI(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url, Audience: "rousseau"}, silentLogger())
	require.NoError(t, err)
	claims := map[string]any{
		"iss": s.url, "sub": "u1", "aud": "rousseau",
		"exp": time.Now().Add(time.Hour).Unix(), "jti": "abc-123",
	}
	id, err := d.VerifyToken(context.Background(), s.signToken(t, claims))
	require.NoError(t, err)
	assert.Equal(t, "jti:"+s.url+":abc-123", id.TokenID)

	delete(claims, "jti")
	tok := s.signToken(t, claims)
	id, err = d.VerifyToken(context.Background(), tok)
	require.NoError(t, err)
	signed := tok[:strings.LastIndex(tok, ".")]
	sum := sha256.Sum256([]byte(signed))
	assert.Equal(t, "sha256:"+hex.EncodeToString(sum[:]), id.TokenID,
		"without jti the key is the signed input, which a re-encoded signature cannot change")
}
