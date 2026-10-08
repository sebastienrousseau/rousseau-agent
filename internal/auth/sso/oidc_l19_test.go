package sso

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// L-19: a JWK's alg / use is bound to the token header, discovery and
// JWKS bodies are bounded, jwks_uri must stay on the issuer's host (or
// an allowlisted one) over https, and a failing refresh backs off while
// the last good keys keep serving for a bounded grace.

// signRS512 signs claims with RS512 under the server's kid.
func signRS512(t *testing.T, s *oidcTestServer, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "RS512", "kid": s.kid, "typ": "JWT"})
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha512Sum(signed)
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.priv, crypto.SHA512, digest[:])
	require.NoError(t, err)
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func liveClaims(s *oidcTestServer) map[string]any {
	return map[string]any{"iss": s.url, "sub": "u1", "exp": time.Now().Add(time.Hour).Unix()}
}

func TestOIDC_KeyAlgIsBoundToHeaderAlg(t *testing.T) {
	s := newOIDCTestServer(t) // key advertises RS256
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url, AllowAnyAudience: true}, silentLogger())
	require.NoError(t, err)

	_, err = d.VerifyToken(context.Background(), signRS512(t, s, liveClaims(s)))
	require.ErrorIs(t, err, ErrTokenInvalid, "an RS256 key must not verify an RS512 token")
	assert.ErrorContains(t, err, "alg")
}

func TestOIDC_EncryptionKeyNeverVerifies(t *testing.T) {
	s := newOIDCTestServer(t)
	s.setKeyMeta("RS256", "enc")
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url, AllowAnyAudience: true}, silentLogger())
	require.NoError(t, err)

	_, err = d.VerifyToken(context.Background(), s.signToken(t, liveClaims(s)))
	require.ErrorIs(t, err, ErrTokenInvalid, "a use=enc key must not verify signatures")
}

// bloatedServer serves discovery and JWKS; the document named by
// bloat gets a 2 MiB padding member.
func bloatedServer(t *testing.T, bloat string) *httptest.Server {
	t.Helper()
	pad := strings.Repeat("A", 2<<20)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{"keys": []any{}}
		kind := "jwks"
		if strings.Contains(r.URL.Path, "openid-configuration") {
			doc = map[string]any{"issuer": srv.URL, "jwks_uri": srv.URL + "/jwks"}
			kind = "discovery"
		}
		if kind == bloat {
			doc["padding"] = pad
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc) //nolint:errcheck // test fixture
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOIDC_DiscoveryAndJWKSBodiesAreBounded(t *testing.T) {
	for _, which := range []string{"discovery", "jwks"} {
		t.Run(which, func(t *testing.T) {
			srv := bloatedServer(t, which)
			d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL, AllowAnyAudience: true}, silentLogger())
			require.NoError(t, err)
			_, err = d.VerifyToken(context.Background(), makeGarbageToken(t, "k"))
			require.Error(t, err)
			assert.ErrorContains(t, err, "too large")
			assert.ErrorContains(t, err, which)
		})
	}
}

func TestCheckJWKSURI(t *testing.T) {
	cases := []struct {
		name, issuer, jwks string
		allowed            []string
		ok                 bool
	}{
		{"same host https", "https://idp.example.com", "https://idp.example.com/keys", nil, true},
		{"same host case-insensitive", "https://IDP.example.com", "https://idp.EXAMPLE.com/keys", nil, true},
		{"foreign host", "https://idp.example.com", "https://evil.example.net/keys", nil, false},
		{"allowlisted host", "https://tenant.okta.com", "https://keys.okta-cdn.com/k", []string{"keys.okta-cdn.com"}, true},
		{"plain http off loopback", "https://idp.example.com", "http://idp.example.com/keys", nil, false},
		{"plain http on loopback", "http://127.0.0.1:8080", "http://127.0.0.1:9090/keys", nil, true},
		{"plain http on localhost", "http://localhost:8080", "http://localhost:8080/keys", nil, true},
		{"other scheme", "https://idp.example.com", "file://idp.example.com/keys", nil, false},
		{"unparseable", "https://idp.example.com", "https://idp.exa\x00mple.com/keys", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkJWKSURI(c.issuer, c.jwks, c.allowed)
			if c.ok {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
		})
	}
}

func TestOIDC_ForeignJWKSURIRejected(t *testing.T) {
	srv := discoveryPointingAt(t, "https://evil.example.net/jwks")
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL, AllowAnyAudience: true}, silentLogger())
	require.NoError(t, err)
	_, err = d.VerifyToken(context.Background(), makeGarbageToken(t, "k"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "jwks_uri")
	assert.Nil(t, d.discovery.Load(), "a rejected discovery document must not be cached")
}

func TestOIDC_ServesLastGoodKeysWhileRefreshFails(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url, JWKSRefresh: 20 * time.Millisecond, AllowAnyAudience: true}, silentLogger())
	require.NoError(t, err)
	tok := s.signToken(t, liveClaims(s))
	_, err = d.VerifyToken(context.Background(), tok)
	require.NoError(t, err)

	s.jwksFail.Store(true)
	time.Sleep(50 * time.Millisecond) // past the TTL
	_, err = d.VerifyToken(context.Background(), tok)
	require.NoError(t, err, "a refresh failure must fall back to the last good keys")
}

func TestOIDC_StaleKeysStopServingAfterGrace(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url, JWKSRefresh: time.Minute, AllowAnyAudience: true}, silentLogger())
	require.NoError(t, err)
	tok := s.signToken(t, liveClaims(s))
	_, err = d.VerifyToken(context.Background(), tok)
	require.NoError(t, err)

	s.jwksFail.Store(true)
	d.jwksMu.Lock()
	d.jwksFetched = time.Now().Add(-(time.Minute + 2*time.Hour))
	d.jwksMu.Unlock()
	_, err = d.VerifyToken(context.Background(), tok)
	require.Error(t, err, "keys more than an hour past their TTL must not be served")
}

func TestOIDC_FailedRefreshBacksOff(t *testing.T) {
	s := newOIDCTestServer(t)
	s.jwksFail.Store(true)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url, AllowAnyAudience: true}, silentLogger())
	require.NoError(t, err)
	tok := s.signToken(t, liveClaims(s))
	for range 3 {
		_, err = d.VerifyToken(context.Background(), tok)
		require.Error(t, err)
	}
	assert.Equal(t, int64(1), s.jwksHits.Load(), "retries inside the backoff window must not reach the IdP")
	assert.ErrorContains(t, err, "backing off")
}
