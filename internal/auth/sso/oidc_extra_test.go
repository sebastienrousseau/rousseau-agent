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
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// b64 is the raw-url base64 encoder used throughout these tests.
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signRawRS256 signs an arbitrary literal `part1` (the token's middle
// segment, verbatim) with an RS256 header. Lets tests craft tokens
// whose signature is VALID yet whose payload segment is malformed —
// exercising VerifyToken's post-verification decode branches.
func signRawRS256(t *testing.T, priv *rsa.PrivateKey, kid, part1 string) string {
	t.Helper()
	hdr, err := json.Marshal(map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"})
	require.NoError(t, err)
	h := b64(hdr)
	signed := h + "." + part1
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	require.NoError(t, err)
	return signed + "." + b64(sig)
}

// -- VerifyToken post-resolve decode branches ----------------------

func TestOIDC_SignatureBase64Invalid(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	tok := s.signToken(t, map[string]any{
		"iss": s.url, "sub": "u1", "exp": time.Now().Add(time.Hour).Unix(),
	})
	// Replace the signature segment with non-base64 so the decode at
	// VerifyToken (before verifySignature) fails.
	lastDot := strings.LastIndex(tok, ".")
	tampered := tok[:lastDot+1] + "!!!not-base64!!!"
	_, err = d.VerifyToken(context.Background(), tampered)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenInvalid)
	assert.Contains(t, err.Error(), "signature base64")
}

func TestOIDC_PayloadBase64Invalid(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	// Sign over a payload segment that is not valid base64. Signature
	// verifies (we signed the literal bytes) but the payload decode
	// afterwards fails.
	tok := signRawRS256(t, s.priv, s.kid, "!!!not-base64!!!")
	_, err = d.VerifyToken(context.Background(), tok)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenInvalid)
	assert.Contains(t, err.Error(), "payload base64")
}

func TestOIDC_PayloadJSONInvalid(t *testing.T) {
	s := newOIDCTestServer(t)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: s.url}, silentLogger())
	require.NoError(t, err)

	// Valid base64 whose decoded bytes are not valid JSON.
	tok := signRawRS256(t, s.priv, s.kid, b64([]byte("not-json")))
	_, err = d.VerifyToken(context.Background(), tok)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenInvalid)
	assert.Contains(t, err.Error(), "payload json")
}

// -- identityFromClaims: raw-payload unmarshal failure -------------

func TestIdentityFromClaims_RawUnmarshalFailsGracefully(t *testing.T) {
	// With TransportMappings configured, identityFromClaims re-parses
	// the raw payload into a map. A non-JSON payload must fall back to
	// the base identity (best-effort) rather than panic or error.
	d, err := NewOIDCDirectory(OIDCConfig{
		Issuer:            "https://issuer.example",
		TransportMappings: []TransportMapping{{Transport: "slack", ClaimKey: "slack_id"}},
	}, silentLogger())
	require.NoError(t, err)

	id := d.identityFromClaims([]byte("not-json"), jwtClaims{Sub: "u-1"})
	assert.Equal(t, "u-1", id.Subject)
	assert.Empty(t, id.TransportIDs, "malformed raw payload yields no transport IDs")
}

// -- fetchDiscovery error branches ---------------------------------

func TestFetchDiscovery_RequestBuildError(t *testing.T) {
	// A control character in the issuer makes http.NewRequestWithContext
	// fail before any network call.
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: "http://exa\x00mple.com"}, silentLogger())
	require.NoError(t, err)
	_, err = d.VerifyToken(context.Background(), makeGarbageToken(t, "k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovery request")
}

func TestFetchDiscovery_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL}, silentLogger())
	require.NoError(t, err)
	_, err = d.VerifyToken(context.Background(), makeGarbageToken(t, "k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovery HTTP 500")
}

func TestFetchDiscovery_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("this is not json"))
	}))
	defer srv.Close()
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL}, silentLogger())
	require.NoError(t, err)
	_, err = d.VerifyToken(context.Background(), makeGarbageToken(t, "k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovery json")
}

// -- refreshJWKS: missing jwks_uri ---------------------------------

func TestRefreshJWKS_MissingJWKSURI(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "openid-configuration") {
			w.Header().Set("Content-Type", "application/json")
			// issuer matches, but no jwks_uri.
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"issuer": srv.URL}))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL}, silentLogger())
	require.NoError(t, err)
	_, err = d.VerifyToken(context.Background(), makeGarbageToken(t, "k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing jwks_uri")
}

// -- fetchJWKS error branches --------------------------------------

// discoveryOnlyServer serves a valid discovery doc pointing jwks_uri at
// the supplied URL. Used to isolate fetchJWKS failure modes.
func discoveryPointingAt(t *testing.T, jwksURI string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "openid-configuration") {
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"issuer":   srv.URL,
				"jwks_uri": jwksURI,
			}))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchJWKS_RequestBuildError(t *testing.T) {
	srv := discoveryPointingAt(t, "http://exa\x00mple.com/jwks")
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL}, silentLogger())
	require.NoError(t, err)
	_, err = d.VerifyToken(context.Background(), makeGarbageToken(t, "k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jwks request")
}

func TestFetchJWKS_FetchError(t *testing.T) {
	srv := discoveryPointingAt(t, "http://127.0.0.1:1/jwks")
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL}, silentLogger())
	require.NoError(t, err)
	_, err = d.VerifyToken(context.Background(), makeGarbageToken(t, "k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jwks fetch")
}

func TestFetchJWKS_Non200(t *testing.T) {
	var jwks *httptest.Server
	jwks = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer jwks.Close()
	srv := discoveryPointingAt(t, jwks.URL)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL}, silentLogger())
	require.NoError(t, err)
	_, err = d.VerifyToken(context.Background(), makeGarbageToken(t, "k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jwks HTTP 500")
	_ = jwks
}

func TestFetchJWKS_BadJSON(t *testing.T) {
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not-json"))
	}))
	defer jwks.Close()
	srv := discoveryPointingAt(t, jwks.URL)
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL}, silentLogger())
	require.NoError(t, err)
	_, err = d.VerifyToken(context.Background(), makeGarbageToken(t, "k"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jwks json")
}

// TestFetchJWKS_SkipsMalformedAndKidlessKeys exercises the two `continue`
// branches in fetchJWKS: a key with an empty kid and a key that fails
// parseJWK are both skipped, while a valid key alongside them still
// produces a working verification.
func TestFetchJWKS_SkipsMalformedAndKidlessKeys(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	const goodKid = "good-1"

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "openid-configuration"):
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"issuer":   srv.URL,
				"jwks_uri": srv.URL + "/jwks",
			}))
		case strings.Contains(r.URL.Path, "jwks"):
			nB64 := b64(priv.N.Bytes())
			eB64 := b64(big.NewInt(int64(priv.E)).Bytes())
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"keys": []any{
					// empty kid → skipped at the kid guard
					map[string]any{"kty": "RSA", "kid": "", "n": nB64, "e": eB64},
					// malformed (bad base64 N) → skipped at parseJWK
					map[string]any{"kty": "RSA", "kid": "bad-1", "n": "!!!", "e": eB64},
					// valid key
					map[string]any{"kty": "RSA", "kid": goodKid, "alg": "RS256", "use": "sig", "n": nB64, "e": eB64},
				},
			}))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL}, silentLogger())
	require.NoError(t, err)

	tok := signRawRS256(t, priv, goodKid, b64(mustJSON(t, map[string]any{
		"iss": srv.URL, "sub": "ok", "exp": time.Now().Add(time.Hour).Unix(),
	})))
	id, err := d.VerifyToken(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, "ok", id.Subject)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// -- resolveKey concurrent double-check ----------------------------

// TestResolveKey_ConcurrentRefreshDoubleCheck drives many goroutines at
// a cold cache behind a deliberately slow JWKS endpoint. The first
// goroutine into the fetching lock performs the refresh; the others,
// on acquiring the lock, hit the re-check branch that returns the
// now-cached key without a second fetch.
func TestResolveKey_ConcurrentRefreshDoubleCheck(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	const kid = "cc-1"

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "openid-configuration"):
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"issuer":   srv.URL,
				"jwks_uri": srv.URL + "/jwks",
			}))
		case strings.Contains(r.URL.Path, "jwks"):
			time.Sleep(80 * time.Millisecond) // widen the double-check window
			nB64 := b64(priv.N.Bytes())
			eB64 := b64(big.NewInt(int64(priv.E)).Bytes())
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"keys": []any{map[string]any{
					"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig", "n": nB64, "e": eB64,
				}},
			}))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	d, err := NewOIDCDirectory(OIDCConfig{Issuer: srv.URL, JWKSRefresh: time.Hour}, silentLogger())
	require.NoError(t, err)

	tok := signRawRS256(t, priv, kid, b64(mustJSON(t, map[string]any{
		"iss": srv.URL, "sub": "cc", "exp": time.Now().Add(time.Hour).Unix(),
	})))

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, e := d.VerifyToken(context.Background(), tok); e != nil {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
}

// -- audience.UnmarshalJSON: bad string branch ---------------------

func TestAudience_UnmarshalBadStringSyntax(t *testing.T) {
	var a audience
	// Leading quote selects the string branch, but the value is a
	// syntactically invalid JSON string (unterminated).
	err := a.UnmarshalJSON([]byte(`"unterminated`))
	assert.Error(t, err)
}

// -- parseJWK: additional EC error branches ------------------------

func TestParseJWK_ECBadYBase64(t *testing.T) {
	_, err := parseJWK(rawJWK{Kty: "EC", Kid: "k", Crv: "P-256", X: "AA", Y: "!!!"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ec y")
}

func TestParseJWK_ECCoordinateTooLarge(t *testing.T) {
	// A 40-byte X exceeds P-256's 32-byte coordinate size.
	big40 := b64(make([]byte, 40))
	_, err := parseJWK(rawJWK{Kty: "EC", Kid: "k", Crv: "P-256", X: big40, Y: "AA"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds curve size")
}

func TestParseJWK_ECPointNotOnCurve(t *testing.T) {
	// Well-formed, correctly-sized, but (0,0) is not a valid point.
	zero := b64(make([]byte, 32))
	_, err := parseJWK(rawJWK{Kty: "EC", Kid: "k", Crv: "P-256", X: zero, Y: zero})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ec parse")
}

func TestParseJWK_ValidEC(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	x := leftPad(priv.X.Bytes(), 32)
	y := leftPad(priv.Y.Bytes(), 32)
	key, err := parseJWK(rawJWK{Kty: "EC", Kid: "k", Alg: "ES256", Crv: "P-256", X: b64(x), Y: b64(y)})
	require.NoError(t, err)
	_, ok := key.pub.(*ecdsa.PublicKey)
	assert.True(t, ok)
}

// -- verifySignature / verifyRSA / verifyECDSA direct branches -----

func TestVerifySignature_UnsupportedAlgDefault(t *testing.T) {
	// The default arm of verifySignature — unreachable via VerifyToken
	// (guarded by supportedAlgs) but covered directly.
	err := verifySignature("HS256", jwkKey{}, []byte("x"), []byte("y"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
}

func TestVerifyRSA_NonRSAKey(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	err = verifyRSA(jwkKey{pub: &priv.PublicKey}, crypto.SHA256, []byte("x"), []byte("y"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-RSA key")
}

func TestVerifySignature_RS384RoundTrip(t *testing.T) {
	// Cover the RS384 dispatch arm end to end.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signed := []byte("header.payload")
	sum := sha512.Sum384(signed)
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA384, sum[:])
	require.NoError(t, err)
	err = verifySignature("RS384", jwkKey{pub: &priv.PublicKey}, signed, sig)
	assert.NoError(t, err)
}

func TestVerifyECDSA_NonECDSAKey(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	err = verifyECDSA(jwkKey{pub: &priv.PublicKey}, sha256.New(), []byte("x"), make([]byte, 64), 32)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-ECDSA key")
}

func TestVerifyECDSA_WrongSignatureLength(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	err = verifyECDSA(jwkKey{pub: &priv.PublicKey}, sha256.New(), []byte("x"), make([]byte, 10), 32)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature length")
}

func TestVerifyECDSA_InvalidSignature(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	// Correct length, but the r||s bytes don't verify.
	err = verifyECDSA(jwkKey{pub: &priv.PublicKey}, sha256.New(), []byte("x"), make([]byte, 64), 32)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature invalid")
}
