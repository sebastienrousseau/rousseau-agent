package sso

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/license"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubChecker implements LicenseCheck with a static answer. Same
// pattern as internal/observability/audit_egress — keeps tests
// decoupled from the licence-loading path.
type stubChecker struct{ enabled bool }

func (s stubChecker) IsEnabled(_ license.Feature) bool { return s.enabled }

func TestNew_UnconfiguredReturnsNopSilently(t *testing.T) {
	// Zero-value Config = "SSO not configured" — vast-majority OSS
	// install. New MUST return Nop with NO log output.
	var logs int64
	logger := slog.New(&countingHandler{count: &logs})
	dir := New(Config{}, stubChecker{enabled: true}, logger)
	_, ok := dir.(Nop)
	require.True(t, ok, "unconfigured SSO must return Nop")
	assert.Zero(t, logs, "unconfigured SSO must NOT log anything")
}

func TestNew_LicenseGateBlocksAndLogsOnce(t *testing.T) {
	// Configured but licence doesn't unlock → Nop + exactly one
	// INFO. Never gates open silently.
	var logs int64
	logger := slog.New(&countingHandler{count: &logs})
	dir := New(
		Config{Kind: KindOIDC, OIDC: OIDCConfig{Issuer: "https://tenant.okta.com"}},
		stubChecker{enabled: false},
		logger,
	)
	_, ok := dir.(Nop)
	require.True(t, ok, "licence-gated feature without licence MUST be Nop")
	assert.EqualValues(t, 1, logs, "licence-required message must fire exactly once")
}

func TestNew_NilCheckerTreatedAsCore(t *testing.T) {
	// Defensive: a nil checker (upstream wiring bug) MUST NOT
	// silently unlock. Same failure mode as "licence missing".
	dir := New(
		Config{Kind: KindOIDC, OIDC: OIDCConfig{Issuer: "https://x"}},
		nil,
		silentLogger(),
	)
	_, ok := dir.(Nop)
	assert.True(t, ok, "nil checker MUST fall back to Nop, never open the gate")
}

func TestNew_LicensedHappyPathReturnsRealDirectory(t *testing.T) {
	dir := New(
		Config{Kind: KindOIDC, OIDC: OIDCConfig{Issuer: "https://tenant.okta.com"}},
		stubChecker{enabled: true},
		silentLogger(),
	)
	_, isNop := dir.(Nop)
	assert.False(t, isNop)
	_, isOIDC := dir.(*OIDCDirectory)
	assert.True(t, isOIDC)
}

func TestNew_BadConfigFallsBackToNop(t *testing.T) {
	// Empty issuer with kind=oidc: constructor fails → Nop + WARN.
	dir := New(
		Config{Kind: KindOIDC},
		stubChecker{enabled: true},
		silentLogger(),
	)
	_, ok := dir.(Nop)
	assert.True(t, ok, "constructor error must fall back to Nop")
}

func TestNew_UnknownKindFallsBackToNop(t *testing.T) {
	dir := New(
		Config{Kind: Kind("saml_broken_typo")},
		stubChecker{enabled: true},
		silentLogger(),
	)
	_, ok := dir.(Nop)
	assert.True(t, ok)
}

func TestNew_NilLoggerDefaults(t *testing.T) {
	// Callers may pass nil — package uses slog.Default.
	dir := New(Config{}, stubChecker{enabled: true}, nil)
	_, ok := dir.(Nop)
	assert.True(t, ok)
}

func TestNop_MethodsReturnDirectoryDisabled(t *testing.T) {
	// The failure sentinel distinguishes "SSO not configured" from
	// "user unknown". Callers CAN then decide whether to fall back
	// to local-auth or reject.
	n := Nop{}
	_, err := n.VerifyToken(context.Background(), "any.token.here")
	assert.ErrorIs(t, err, ErrDirectoryDisabled)
	_, err = n.ResolveTransportID(context.Background(), "slack", "U012")
	assert.ErrorIs(t, err, ErrDirectoryDisabled)
}

// -- OIDC verification integration --------------------------------

// oidcTestServer mints RSA keys, serves the OIDC discovery +
// JWKS documents, and hands out a helper to sign tokens with the
// same private key. Every verification test builds one, wires the
// OIDCDirectory at server.URL, and asserts on real signature
// verification (no mocks).
type oidcTestServer struct {
	priv *rsa.PrivateKey
	pub  *rsa.PublicKey
	kid  string
	url  string
	srv  *httptest.Server
	// counters for cache-behaviour assertions
	discoveryHits atomic.Int64
	jwksHits      atomic.Int64
}

func newOIDCTestServer(t *testing.T) *oidcTestServer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	s := &oidcTestServer{
		priv: priv,
		pub:  &priv.PublicKey,
		kid:  "test-key-1",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		s.discoveryHits.Add(1)
		doc := map[string]any{
			"issuer":   s.url,
			"jwks_uri": s.url + "/.well-known/jwks.json",
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(doc))
	})
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		s.jwksHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		nB64 := base64.RawURLEncoding.EncodeToString(priv.N.Bytes())
		eB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes())
		doc := map[string]any{
			"keys": []any{
				map[string]any{
					"kty": "RSA", "kid": s.kid, "alg": "RS256", "use": "sig",
					"n": nB64, "e": eB64,
				},
			},
		}
		require.NoError(t, json.NewEncoder(w).Encode(doc))
	})
	s.srv = httptest.NewServer(mux)
	s.url = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

// signToken produces an RS256-signed JWT with the given claims,
// using the server's private key + kid.
func (s *oidcTestServer) signToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	return signRS256Token(t, s.priv, s.kid, claims)
}

func signRS256Token(t *testing.T, priv *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"})
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	h := base64.RawURLEncoding.EncodeToString(header)
	p := base64.RawURLEncoding.EncodeToString(payload)
	signed := h + "." + p
	hash := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hash[:])
	require.NoError(t, err)
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestOIDC_ConstructorRejectsEmptyIssuer(t *testing.T) {
	_, err := NewOIDCDirectory(OIDCConfig{}, silentLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issuer")
}

func TestOIDC_NilLoggerDefaults(t *testing.T) {
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: "https://x"}, nil)
	require.NoError(t, err)
	assert.NotNil(t, d)
}

func TestOIDC_HappyPath_VerifiesValidToken(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{
		Issuer:   s.url,
		Audience: "rousseau",
	}, silentLogger())
	require.NoError(t, err)

	tok := s.signToken(t, map[string]any{
		"iss":            s.url,
		"sub":            "auth0|abc123",
		"aud":            "rousseau",
		"exp":            time.Now().Add(1 * time.Hour).Unix(),
		"iat":            time.Now().Unix(),
		"email":          "alice@example.com",
		"email_verified": true,
		"name":           "Alice Example",
		"groups":         []string{"eng", "sre"},
	})

	id, err := d.VerifyToken(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, "auth0|abc123", id.Subject)
	assert.Equal(t, "alice@example.com", id.Email)
	assert.True(t, id.EmailVerified)
	assert.Equal(t, "Alice Example", id.DisplayName)
	assert.ElementsMatch(t, []string{"eng", "sre"}, id.Groups)
	assert.False(t, id.ExpiresAt.IsZero())
}

func TestOIDC_TransportMappingsResolveCustomClaims(t *testing.T) {
	// Custom-namespaced claims (Okta's "https://schemas..." pattern)
	// get pulled into Identity.TransportIDs via TransportMapping.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{
		Issuer: s.url,
		TransportMappings: []TransportMapping{
			{Transport: "slack", ClaimKey: "slack_user_id"},
			{Transport: "matrix", ClaimKey: "matrix_mxid"},
		},
	}, silentLogger())
	require.NoError(t, err)

	tok := s.signToken(t, map[string]any{
		"iss":           s.url,
		"sub":           "user-99",
		"exp":           time.Now().Add(time.Hour).Unix(),
		"slack_user_id": "U012ABCDE",
		"matrix_mxid":   "@alice:example.com",
		"other_random":  "ignored",
	})

	id, err := d.VerifyToken(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, "U012ABCDE", id.TransportIDs["slack"])
	assert.Equal(t, "@alice:example.com", id.TransportIDs["matrix"])
	assert.Len(t, id.TransportIDs, 2, "only configured mappings populate")
}

func TestOIDC_ExpiredTokenRejected(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	tok := s.signToken(t, map[string]any{
		"iss": s.url,
		"sub": "u1",
		"exp": time.Now().Add(-10 * time.Minute).Unix(),
	})
	_, err = d.VerifyToken(context.Background(), tok)
	assert.ErrorIs(t, err, ErrTokenExpired)
}

func TestOIDC_NotYetValidRejected(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	tok := s.signToken(t, map[string]any{
		"iss": s.url,
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
		"nbf": time.Now().Add(10 * time.Minute).Unix(),
	})
	_, err = d.VerifyToken(context.Background(), tok)
	assert.ErrorIs(t, err, ErrTokenExpired)
}

func TestOIDC_ClockSkewToleratesRecentExpiry(t *testing.T) {
	// A token that expired 30 seconds ago is still accepted under
	// the default 2-minute skew — matches typical NTP drift on
	// corporate networks.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)
	tok := s.signToken(t, map[string]any{
		"iss": s.url,
		"sub": "u1",
		"exp": time.Now().Add(-30 * time.Second).Unix(),
	})
	_, err = d.VerifyToken(context.Background(), tok)
	assert.NoError(t, err, "30s expired < 2m skew must be accepted")
}

func TestOIDC_AudienceMismatchRejected(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{
		Issuer:   s.url,
		Audience: "rousseau",
	}, silentLogger())
	require.NoError(t, err)

	tok := s.signToken(t, map[string]any{
		"iss": s.url,
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
		"aud": "some-other-app",
	})
	_, err = d.VerifyToken(context.Background(), tok)
	assert.ErrorIs(t, err, ErrAudienceMismatch)
}

func TestOIDC_AudienceAsArrayAccepted(t *testing.T) {
	// RFC 7519 permits aud as either a string or an array. IdPs
	// disagree — verifier must accept both.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{
		Issuer:   s.url,
		Audience: "rousseau",
	}, silentLogger())
	require.NoError(t, err)

	tok := s.signToken(t, map[string]any{
		"iss": s.url,
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
		"aud": []string{"other-app", "rousseau", "third-app"},
	})
	_, err = d.VerifyToken(context.Background(), tok)
	assert.NoError(t, err, "aud array containing configured value must be accepted")
}

func TestOIDC_IssuerMismatchRejected(t *testing.T) {
	// A token minted by a different Okta tenant must NOT verify —
	// even if the signature is technically valid. Prevents cross-
	// tenant token replay.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	tok := s.signToken(t, map[string]any{
		"iss": "https://different-tenant.okta.com",
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err = d.VerifyToken(context.Background(), tok)
	assert.ErrorIs(t, err, ErrIssuerMismatch)
}

func TestOIDC_TamperedSignatureRejected(t *testing.T) {
	// Flip one byte in the signature → verification MUST fail.
	// This is the load-bearing test for "no HS none / algorithm
	// confusion" attacks.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	tok := s.signToken(t, map[string]any{
		"iss": s.url,
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	// Decode the signature, flip a middle byte, re-encode. Flipping
	// the last base64 char is unreliable because trailing bits can
	// map multiple chars to the same decoded bytes.
	lastDot := strings.LastIndex(tok, ".")
	require.Greater(t, lastDot, 0)
	sig, err := base64.RawURLEncoding.DecodeString(tok[lastDot+1:])
	require.NoError(t, err)
	require.Greater(t, len(sig), 16)
	sig[len(sig)/2] ^= 0xFF
	tampered := tok[:lastDot+1] + base64.RawURLEncoding.EncodeToString(sig)
	_, err = d.VerifyToken(context.Background(), tampered)
	assert.ErrorIs(t, err, ErrTokenInvalid)
}

func TestOIDC_UnsupportedAlgRejected(t *testing.T) {
	// HS256 / none must NEVER verify — even against a well-known
	// key. This is THE historical JWT footgun; the test locks
	// against it.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	// Hand-craft a "none"-alg token.
	hdr, err := json.Marshal(map[string]any{"alg": "none", "kid": s.kid, "typ": "JWT"})
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"iss": s.url, "sub": "attacker", "exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)
	tok := base64.RawURLEncoding.EncodeToString(hdr) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "."
	_, err = d.VerifyToken(context.Background(), tok)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenInvalid)
	assert.Contains(t, err.Error(), "alg")
}

func TestOIDC_MalformedTokens(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	cases := []struct {
		name, token string
	}{
		{"single-part", "onlyone"},
		{"four-parts", "a.b.c.d"},
		{"bad-header-base64", "!!!.aaaa.aaaa"},
		{"bad-header-json", base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".aaaa.aaaa"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.VerifyToken(context.Background(), tc.token)
			assert.ErrorIs(t, err, ErrTokenInvalid)
		})
	}
}

func TestOIDC_MissingKidRejected(t *testing.T) {
	// A token with no `kid` header can't be matched against a
	// multi-key JWKS. Reject with a legible error rather than
	// guessing.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	hdr, err := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT"}) // no kid
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"iss": s.url, "sub": "u1", "exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)
	h := base64.RawURLEncoding.EncodeToString(hdr)
	p := base64.RawURLEncoding.EncodeToString(payload)
	signed := h + "." + p
	hash := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.priv, crypto.SHA256, hash[:])
	require.NoError(t, err)
	tok := signed + "." + base64.RawURLEncoding.EncodeToString(sig)

	_, err = d.VerifyToken(context.Background(), tok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kid")
}

func TestOIDC_UnknownKidTriggersJWKSRefresh(t *testing.T) {
	// A token with a kid not in the current JWKS cache should
	// trigger a re-fetch — IdPs rotate keys on their own schedule.
	// After the refresh, if the kid still isn't there, reject.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{
		Issuer:      s.url,
		JWKSRefresh: 1 * time.Hour, // long TTL so we can observe the forced refresh
	}, silentLogger())
	require.NoError(t, err)

	// Seed the cache with one valid verification.
	good := s.signToken(t, map[string]any{
		"iss": s.url, "sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err = d.VerifyToken(context.Background(), good)
	require.NoError(t, err)
	initialJWKSHits := s.jwksHits.Load()

	// Sign a token with a kid the server never returns.
	tokBadKid := signRS256Token(t, s.priv, "not-a-real-kid", map[string]any{
		"iss": s.url, "sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err = d.VerifyToken(context.Background(), tokBadKid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kid")
	assert.Greater(t, s.jwksHits.Load(), initialJWKSHits, "unknown kid must trigger JWKS refresh")
}

func TestOIDC_JWKSCachedAcrossCalls(t *testing.T) {
	// Two consecutive VerifyToken calls with the same kid within
	// the refresh TTL must hit the JWKS endpoint exactly ONCE.
	// Prevents thundering herd against the IdP.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{
		Issuer:      s.url,
		JWKSRefresh: 1 * time.Hour,
	}, silentLogger())
	require.NoError(t, err)

	tok := s.signToken(t, map[string]any{
		"iss": s.url, "sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err = d.VerifyToken(context.Background(), tok)
	require.NoError(t, err)
	firstHit := s.jwksHits.Load()

	// Ten more calls, same kid — should not fetch again.
	for i := 0; i < 10; i++ {
		_, err = d.VerifyToken(context.Background(), tok)
		require.NoError(t, err)
	}
	assert.Equal(t, firstHit, s.jwksHits.Load(), "same-kid calls within TTL must not re-fetch JWKS")
}

func TestOIDC_DiscoveryErrorFailsClosed(t *testing.T) {
	// A dead / bad discovery endpoint means we can't verify
	// anything. Verification must fail (never fall back to
	// "unverified but accepted") — SSO fails CLOSED.
	d, err := NewOIDCDirectory(OIDCConfig{
		Issuer: "http://127.0.0.1:1", // guaranteed no listener
	}, silentLogger())
	require.NoError(t, err)

	tok := makeGarbageToken(t, "kid-1")
	_, err = d.VerifyToken(context.Background(), tok)
	require.Error(t, err)
	// Message contains "discovery" or "connection refused" — the
	// specific string depends on the OS but SOMETHING legible
	// makes it out.
	assert.NotContains(t, err.Error(), "signature invalid", "must fail on discovery, not silently accept")
}

func TestOIDC_DiscoveryIssuerMismatchRejected(t *testing.T) {
	// The discovery document's `issuer` field must match the
	// configured issuer. Prevents an attacker who controls DNS
	// / routing from pointing at a look-alike IdP.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "openid-configuration") {
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"issuer":   "https://impersonator.example.com",
				"jwks_uri": "https://impersonator.example.com/jwks",
			}))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL}, silentLogger())
	require.NoError(t, err)

	tok := makeGarbageToken(t, "kid-1")
	_, err = d.VerifyToken(context.Background(), tok)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovery issuer mismatch")
}

func TestOIDC_RS512TokenVerifies(t *testing.T) {
	// Some IdPs (e.g. tenant policies on Okta) mint RS512. Cover
	// the SHA-512 branch of verifySignature.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	header, err := json.Marshal(map[string]any{"alg": "RS512", "kid": s.kid, "typ": "JWT"})
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"iss": s.url, "sub": "u-rs512",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)
	h := base64.RawURLEncoding.EncodeToString(header)
	p := base64.RawURLEncoding.EncodeToString(payload)
	signed := h + "." + p
	digest := sha512Sum(signed)
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.priv, crypto.SHA512, digest[:])
	require.NoError(t, err)
	tok := signed + "." + base64.RawURLEncoding.EncodeToString(sig)

	id, err := d.VerifyToken(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, "u-rs512", id.Subject)
}

func TestOIDC_ES384TokenVerifies(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	srv := ecdsaTestServer(t, priv, "ec-384", "P-384", "ES384")
	defer srv.Close()

	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL}, silentLogger())
	require.NoError(t, err)

	header, err := json.Marshal(map[string]any{"alg": "ES384", "kid": "ec-384", "typ": "JWT"})
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"iss": srv.URL, "sub": "u-es384",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)
	h := base64.RawURLEncoding.EncodeToString(header)
	p := base64.RawURLEncoding.EncodeToString(payload)
	signed := h + "." + p
	digest := sha384Sum(signed)
	r, si, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	require.NoError(t, err)
	sig := make([]byte, 0, 96)
	sig = append(sig, leftPad(r.Bytes(), 48)...)
	sig = append(sig, leftPad(si.Bytes(), 48)...)
	tok := signed + "." + base64.RawURLEncoding.EncodeToString(sig)

	id, err := d.VerifyToken(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, "u-es384", id.Subject)
}

func TestParseJWK_UnsupportedCurveRejected(t *testing.T) {
	// P-521 isn't in supportedAlgs — reject at parse.
	_, err := parseJWK(rawJWK{Kty: "EC", Kid: "k", Crv: "P-521", X: "AA", Y: "AA"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "P-521")
}

func TestOIDC_ES256TokenVerifies(t *testing.T) {
	// Confirms the ECDSA path (used by Google Workspace + some
	// Auth0 tenants) validates a real ES256 signature.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	srv := ecdsaTestServer(t, priv, "ec-kid-1", "P-256", "ES256")
	defer srv.Close()

	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL}, silentLogger())
	require.NoError(t, err)

	tok := signES256Token(t, priv, "ec-kid-1", map[string]any{
		"iss": srv.URL,
		"sub": "user-ec",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	id, err := d.VerifyToken(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, "user-ec", id.Subject)
}

func TestOIDC_ResolveTransportIDWithoutStoreReturnsNotFound(t *testing.T) {
	// No DirectoryStore wired → matches pre-#132 behaviour so
	// operators who run OIDC-only get a legible ErrNotFound
	// rather than a nil-dereference panic.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)
	_, err = d.ResolveTransportID(context.Background(), "slack", "U012")
	assert.ErrorIs(t, err, ErrNotFound)
}

// stubDirectoryStore is a controllable DirectoryStore for the
// wire-up tests.
type stubDirectoryStore struct {
	users map[string]Identity
	err   error
}

func (s *stubDirectoryStore) ResolveExternalID(_ context.Context, externalID string) (Identity, error) {
	if s.err != nil {
		return Identity{}, s.err
	}
	id, ok := s.users[externalID]
	if !ok {
		return Identity{}, ErrNotFound
	}
	return id, nil
}

func TestOIDC_ResolveTransportIDWithStoreReturnsIdentity(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	d.WithStore(&stubDirectoryStore{
		users: map[string]Identity{
			"okta|alice-external-id": {
				Subject:     "scim-user-1",
				DisplayName: "Alice",
				Groups:      []string{"eng", "sre"},
			},
		},
	})

	id, err := d.ResolveTransportID(context.Background(), "slack", "okta|alice-external-id")
	require.NoError(t, err)
	assert.Equal(t, "scim-user-1", id.Subject)
	assert.Equal(t, "Alice", id.DisplayName)
	assert.ElementsMatch(t, []string{"eng", "sre"}, id.Groups)
}

func TestOIDC_ResolveTransportIDStoreErrorPropagates(t *testing.T) {
	// Store-side errors surface as-is so callers can
	// distinguish "user not found" from "backend broken".
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	d.WithStore(&stubDirectoryStore{err: errors.New("directory offline")})
	_, err = d.ResolveTransportID(context.Background(), "slack", "any-id")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotFound, "backend errors must NOT collapse into ErrNotFound")
}

func TestOIDC_WithStoreNilIsSafe(t *testing.T) {
	// Property: passing a nil store returns the directory
	// unchanged. Matches wrap-with-Option pattern used across
	// the wrappers.
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)
	require.NotPanics(t, func() {
		d.WithStore(nil)
	})
	_, err = d.ResolveTransportID(context.Background(), "slack", "any")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestAudience_UnmarshalHandlesBothShapes(t *testing.T) {
	// Directly exercise the audience.UnmarshalJSON branches so
	// bad-input handling is locked.
	cases := []struct {
		in   string
		want audience
	}{
		{`"one"`, audience{"one"}},
		{`["a","b"]`, audience{"a", "b"}},
	}
	for _, tc := range cases {
		var a audience
		require.NoError(t, a.UnmarshalJSON([]byte(tc.in)))
		assert.Equal(t, tc.want, a)
	}

	var a audience
	assert.Error(t, a.UnmarshalJSON([]byte(`{}`)), "object is neither a string nor a []string")
	assert.NoError(t, a.UnmarshalJSON(nil), "empty input is a no-op")
}

func TestParseJWK_UnsupportedKty(t *testing.T) {
	_, err := parseJWK(rawJWK{Kty: "unicorn", Kid: "k"})
	assert.Error(t, err)
}

func TestParseJWK_MalformedRSAAndEC(t *testing.T) {
	// Bad base64 in N/E for RSA and X/Y for EC must reject.
	_, err := parseJWK(rawJWK{Kty: "RSA", Kid: "k", N: "!!!", E: "AQAB"})
	assert.Error(t, err)
	_, err = parseJWK(rawJWK{Kty: "RSA", Kid: "k", N: "AA", E: "!!!"})
	assert.Error(t, err)
	_, err = parseJWK(rawJWK{Kty: "RSA", Kid: "k"})
	assert.Error(t, err, "empty N / E must reject")

	_, err = parseJWK(rawJWK{Kty: "EC", Kid: "k", Crv: "P-256", X: "!!!"})
	assert.Error(t, err)
	_, err = parseJWK(rawJWK{Kty: "EC", Kid: "k", Crv: "not-a-curve", X: "AA", Y: "AA"})
	assert.Error(t, err)
}

// -- helpers ------------------------------------------------------

// makeGarbageToken returns a JWT-shaped string that will not verify
// (unsigned "content"). Used by tests that assert on discovery-
// stage errors before signature verification runs.
func makeGarbageToken(t *testing.T, kid string) string {
	t.Helper()
	hdr, err := json.Marshal(map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"})
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"iss": "irrelevant", "sub": "u", "exp": time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(hdr) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("not-a-real-signature"))
}

// ecdsaTestServer serves discovery + JWKS for a single ECDSA key.
// Mirrors newOIDCTestServer for the ES256 test.
func ecdsaTestServer(t *testing.T, priv *ecdsa.PrivateKey, kid, crv, alg string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"issuer":   srv.URL,
			"jwks_uri": srv.URL + "/.well-known/jwks.json",
		}))
	})
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Left-pad X and Y to the curve byte-size so serialisation
		// matches the wire spec.
		byteSize := (priv.Curve.Params().BitSize + 7) / 8
		x := leftPad(priv.X.Bytes(), byteSize)
		y := leftPad(priv.Y.Bytes(), byteSize)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"keys": []any{map[string]any{
				"kty": "EC", "kid": kid, "alg": alg, "use": "sig", "crv": crv,
				"x": base64.RawURLEncoding.EncodeToString(x),
				"y": base64.RawURLEncoding.EncodeToString(y),
			}},
		}))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func signES256Token(t *testing.T, priv *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "ES256", "kid": kid, "typ": "JWT"})
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	h := base64.RawURLEncoding.EncodeToString(header)
	p := base64.RawURLEncoding.EncodeToString(payload)
	signed := h + "." + p
	hash := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, priv, hash[:])
	require.NoError(t, err)
	byteSize := 32 // P-256
	sig := make([]byte, 0, 2*byteSize)
	sig = append(sig, leftPad(r.Bytes(), byteSize)...)
	sig = append(sig, leftPad(s.Bytes(), byteSize)...)
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func sha512Sum(s string) [64]byte {
	return sha512Impl([]byte(s))
}

func sha384Sum(s string) [48]byte {
	h := sha512.New384()
	h.Write([]byte(s))
	var out [48]byte
	copy(out[:], h.Sum(nil))
	return out
}

func sha512Impl(b []byte) [64]byte {
	h := sha512.New()
	h.Write(b)
	var out [64]byte
	copy(out[:], h.Sum(nil))
	return out
}

func leftPad(b []byte, size int) []byte {
	if len(b) >= size {
		return b
	}
	out := make([]byte, size)
	copy(out[size-len(b):], b)
	return out
}

// countingHandler is a slog.Handler wrapper that atomically counts
// invocations. Used to assert "logs exactly once" semantics.
type countingHandler struct {
	count *int64
}

func (h *countingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, _ slog.Record) error {
	atomic.AddInt64(h.count, 1)
	return nil
}
func (h *countingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(_ string) slog.Handler      { return h }

// unused-value suppressor: keep golangci-lint quiet about the
// documented "we know it's an int" cast in identityFromClaims.
var _ = fmt.Sprintf
