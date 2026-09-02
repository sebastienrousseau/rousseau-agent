package sso

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OIDCDirectory verifies JWT bearer tokens against an OIDC issuer's
// discovery document + JWKS. Zero external deps: everything below is
// stdlib crypto + net/http + encoding/json. Matches the rest of the
// codebase's "small surface, few deps, easy to audit" DNA.
//
// # Supported signature algorithms
//
// RS256 / RS384 / RS512 (RSA-PKCS1v15) and ES256 / ES384
// (ECDSA on P-256 / P-384). These cover every major IdP; ES512 and
// PS* algorithms exist in the spec but haven't been requested and
// each new algorithm is a small surface addition.
//
// HS* (HMAC) is deliberately unsupported — HS-signed tokens require
// the verifier to hold the shared secret, which conflates issuer +
// verifier trust and turns the JWKS URI into decoration. RS256 is
// the OIDC-recommended default anyway.
type OIDCDirectory struct {
	cfg    OIDCConfig
	client *http.Client
	logger *slog.Logger
	// store, when non-nil, satisfies ResolveTransportID by
	// looking up the transport identifier in an operator-
	// configured directory (typically SCIM-populated). Wired
	// via [OIDCDirectory.WithStore] at assembly time; nil
	// falls back to ErrNotFound (matches the pre-#132 pilot
	// behaviour).
	store DirectoryStore

	// Discovery document fields, populated on first successful
	// fetch and refreshed with the JWKS.
	discovery atomic.Pointer[discoveryDoc]

	// JWKS cache. Fetched lazily on first VerifyToken; refreshed
	// on cfg.JWKSRefresh cadence OR immediately when an unknown
	// `kid` is seen (the IdP has rotated between our polls).
	jwksMu       sync.RWMutex
	jwks         map[string]jwkKey // kid → parsed key
	jwksFetched  time.Time
	jwksFetching sync.Mutex // serialises concurrent refreshes
}

// NewOIDCDirectory constructs a running verifier. Does NOT hit the
// network — discovery + JWKS fetches happen lazily on first
// VerifyToken. That way a mis-typed issuer is a runtime deferred
// error rather than a boot-time hard failure (matches the fail-
// open discipline documented on the package).
func NewOIDCDirectory(cfg OIDCConfig, logger *slog.Logger) (*OIDCDirectory, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("sso/oidc: issuer is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg = cfg.applyDefaults()
	return &OIDCDirectory{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: logger,
		jwks:   map[string]jwkKey{},
	}, nil
}

// VerifyToken satisfies [Directory]. Parses the JWT, fetches / uses
// the cached JWKS, verifies signature + claims, and returns the
// resulting Identity.
func (d *OIDCDirectory) VerifyToken(ctx context.Context, token string) (Identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, fmt.Errorf("%w: want header.payload.signature", ErrTokenInvalid)
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Identity{}, fmt.Errorf("%w: header base64: %s", ErrTokenInvalid, err.Error())
	}
	var hdr jwtHeader
	if err := json.Unmarshal(headerJSON, &hdr); err != nil {
		return Identity{}, fmt.Errorf("%w: header json: %s", ErrTokenInvalid, err.Error())
	}
	if hdr.Kid == "" {
		return Identity{}, fmt.Errorf("%w: missing kid header", ErrTokenInvalid)
	}
	if _, ok := supportedAlgs[hdr.Alg]; !ok {
		return Identity{}, fmt.Errorf("%w: unsupported alg %q", ErrTokenInvalid, hdr.Alg)
	}

	key, err := d.resolveKey(ctx, hdr.Kid)
	if err != nil {
		return Identity{}, err
	}
	// Signature verification. Signed input is the raw
	// "header.payload" bytes — never re-derive from parsed JSON.
	signed := []byte(parts[0] + "." + parts[1])
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Identity{}, fmt.Errorf("%w: signature base64: %s", ErrTokenInvalid, err.Error())
	}
	if err := verifySignature(hdr.Alg, key, signed, sig); err != nil {
		return Identity{}, fmt.Errorf("%w: %s", ErrTokenInvalid, err.Error())
	}

	// Payload — decode after signature verification, never before,
	// so a malformed-payload attacker can't tell "wrong signature"
	// from "wrong claims" (defence against timing oracles).
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, fmt.Errorf("%w: payload base64: %s", ErrTokenInvalid, err.Error())
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Identity{}, fmt.Errorf("%w: payload json: %s", ErrTokenInvalid, err.Error())
	}

	now := time.Now()
	if err := d.checkClaims(claims, now); err != nil {
		return Identity{}, err
	}
	return d.identityFromClaims(payload, claims), nil
}

// ResolveTransportID looks up externalID in the operator-
// configured [DirectoryStore] (typically the SCIM-populated
// store wired at daemon assembly). Returns [ErrNotFound] when
// no store is configured OR the identifier isn't in it.
//
// The `transport` argument is currently unused — the SCIM
// externalId column is a scalar the IdP populates; convention
// so far is that the IdP encodes any transport disambiguation
// into that value (e.g. `slack:U012ABC`). A future backend
// with per-transport columns can start honouring the argument
// without breaking the interface.
func (d *OIDCDirectory) ResolveTransportID(ctx context.Context, transport, externalID string) (Identity, error) {
	_ = transport
	if d.store == nil {
		return Identity{}, ErrNotFound
	}
	return d.store.ResolveExternalID(ctx, externalID)
}

// checkClaims runs the three RFC 7519 time checks (exp, nbf, iat)
// plus the OIDC iss / aud checks. Ordering matters: cheapest checks
// first so an expired token doesn't cost a string compare.
func (d *OIDCDirectory) checkClaims(c jwtClaims, now time.Time) error {
	skew := d.cfg.ClockSkew
	if c.Exp > 0 && now.Add(-skew).Unix() > c.Exp {
		return ErrTokenExpired
	}
	if c.Nbf > 0 && now.Add(skew).Unix() < c.Nbf {
		return ErrTokenExpired
	}
	if c.Iss != "" && c.Iss != d.cfg.Issuer {
		return fmt.Errorf("%w: got %q want %q", ErrIssuerMismatch, c.Iss, d.cfg.Issuer)
	}
	if d.cfg.Audience != "" && !c.matchAudience(d.cfg.Audience) {
		return fmt.Errorf("%w: got %v want %q", ErrAudienceMismatch, c.Aud, d.cfg.Audience)
	}
	return nil
}

// identityFromClaims lifts the standard OIDC claims into an
// [Identity] and — for each configured [TransportMapping] — looks up
// the transport-native ID from the raw JSON payload.
func (d *OIDCDirectory) identityFromClaims(payload []byte, c jwtClaims) Identity {
	id := Identity{
		Subject:       c.Sub,
		Email:         c.Email,
		EmailVerified: c.EmailVerified,
		DisplayName:   c.Name,
		Groups:        c.Groups,
	}
	if c.Exp > 0 {
		id.ExpiresAt = time.Unix(c.Exp, 0).UTC()
	}
	if len(d.cfg.TransportMappings) == 0 {
		return id
	}
	// Look up transport claim keys from the raw payload — some IdPs
	// use custom-namespaced attribute names ("https://schemas.
	// rousseau/slack_id") that jwtClaims doesn't statically know
	// about.
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return id // best-effort; identity still valid without transport IDs
	}
	id.TransportIDs = make(map[string]string, len(d.cfg.TransportMappings))
	for _, m := range d.cfg.TransportMappings {
		if v, ok := raw[m.ClaimKey].(string); ok && v != "" {
			id.TransportIDs[m.Transport] = v
		}
	}
	return id
}

// resolveKey returns the JWK matching kid, refreshing the JWKS
// cache if the kid is unknown OR the cache TTL has elapsed. Serialises
// concurrent refresh attempts so ten simultaneous unknown-kid tokens
// don't stampede the IdP.
func (d *OIDCDirectory) resolveKey(ctx context.Context, kid string) (jwkKey, error) {
	d.jwksMu.RLock()
	k, ok := d.jwks[kid]
	fetched := d.jwksFetched
	d.jwksMu.RUnlock()
	if ok && time.Since(fetched) < d.cfg.JWKSRefresh {
		return k, nil
	}

	d.jwksFetching.Lock()
	defer d.jwksFetching.Unlock()
	// Re-check under the fetching lock — another goroutine may have
	// refreshed while we were queuing.
	d.jwksMu.RLock()
	k, ok = d.jwks[kid]
	fetched = d.jwksFetched
	d.jwksMu.RUnlock()
	if ok && time.Since(fetched) < d.cfg.JWKSRefresh {
		return k, nil
	}

	if err := d.refreshJWKS(ctx); err != nil {
		return jwkKey{}, err
	}
	d.jwksMu.RLock()
	k, ok = d.jwks[kid]
	d.jwksMu.RUnlock()
	if !ok {
		return jwkKey{}, fmt.Errorf("%w: kid %q not in JWKS", ErrTokenInvalid, kid)
	}
	return k, nil
}

// refreshJWKS discovers the JWKS URI (if needed) then fetches +
// parses the JWKS document. Caller must hold jwksFetching.
func (d *OIDCDirectory) refreshJWKS(ctx context.Context) error {
	disc := d.discovery.Load()
	if disc == nil {
		fresh, err := d.fetchDiscovery(ctx)
		if err != nil {
			return err
		}
		d.discovery.Store(fresh)
		disc = fresh
	}
	if disc.JWKSURI == "" {
		return fmt.Errorf("sso/oidc: discovery document missing jwks_uri")
	}
	fresh, err := d.fetchJWKS(ctx, disc.JWKSURI)
	if err != nil {
		return err
	}
	d.jwksMu.Lock()
	d.jwks = fresh
	d.jwksFetched = time.Now()
	d.jwksMu.Unlock()
	d.logger.Info("sso.jwks_refreshed",
		slog.String("jwks_uri", disc.JWKSURI),
		slog.Int("keys", len(fresh)),
	)
	return nil
}

// fetchDiscovery pulls the standard OIDC discovery document.
func (d *OIDCDirectory) fetchDiscovery(ctx context.Context) (*discoveryDoc, error) {
	url := strings.TrimRight(d.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("sso/oidc: discovery request: %w", err)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sso/oidc: discovery fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sso/oidc: discovery HTTP %d", resp.StatusCode)
	}
	var doc discoveryDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("sso/oidc: discovery json: %w", err)
	}
	// Cross-check that the returned issuer matches the configured
	// one — belt-and-braces against a MITM'd discovery response.
	if doc.Issuer != "" && doc.Issuer != d.cfg.Issuer {
		return nil, fmt.Errorf("sso/oidc: discovery issuer mismatch: got %q want %q", doc.Issuer, d.cfg.Issuer)
	}
	return &doc, nil
}

// fetchJWKS pulls the JWKS document from url and parses every key
// this package supports. Unsupported keys are skipped with a Debug
// log rather than failing the whole refresh (IdPs sometimes serve
// keys with unusual algorithms).
func (d *OIDCDirectory) fetchJWKS(ctx context.Context, url string) (map[string]jwkKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("sso/oidc: jwks request: %w", err)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sso/oidc: jwks fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sso/oidc: jwks HTTP %d", resp.StatusCode)
	}
	var jwks jwksDocument
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("sso/oidc: jwks json: %w", err)
	}
	out := make(map[string]jwkKey, len(jwks.Keys))
	for _, raw := range jwks.Keys {
		if raw.Kid == "" {
			continue
		}
		key, err := parseJWK(raw)
		if err != nil {
			d.logger.Debug("sso.jwks_key_skipped",
				slog.String("kid", raw.Kid),
				slog.String("err", err.Error()),
			)
			continue
		}
		out[raw.Kid] = key
	}
	return out, nil
}

// -- wire types & crypto helpers -------------------------------------------

// supportedAlgs is the whitelist of JWT `alg` values this package
// accepts. Kept as a package-level lookup so verifySignature +
// header parsing use the same source of truth.
var supportedAlgs = map[string]struct{}{
	"RS256": {}, "RS384": {}, "RS512": {},
	"ES256": {}, "ES384": {},
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ,omitempty"`
}

// jwtClaims covers the OIDC standard claims. Custom / transport-
// mapping claims are read separately via json.Unmarshal into a
// map[string]any (see identityFromClaims).
type jwtClaims struct {
	Iss           string   `json:"iss"`
	Sub           string   `json:"sub"`
	Aud           audience `json:"aud"`
	Exp           int64    `json:"exp"`
	Iat           int64    `json:"iat"`
	Nbf           int64    `json:"nbf"`
	Email         string   `json:"email"`
	EmailVerified bool     `json:"email_verified"`
	Name          string   `json:"name"`
	Groups        []string `json:"groups"`
}

// audience handles the JWT audience claim's dual shape: a bare
// string OR an array of strings. RFC 7519 permits both, and IdPs
// disagree on which they use.
type audience []string

// UnmarshalJSON implements json.Unmarshaler.
func (a *audience) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*a = audience{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	*a = arr
	return nil
}

// matchAudience reports whether want appears in the claim.
func (c jwtClaims) matchAudience(want string) bool {
	for _, v := range c.Aud {
		if v == want {
			return true
		}
	}
	return false
}

type discoveryDoc struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

type jwksDocument struct {
	Keys []rawJWK `json:"keys"`
}

// rawJWK is the on-the-wire JWK shape. Fields present depend on the
// key type (`kty`). Everything is stored as base64url strings.
type rawJWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	// RSA fields (kty == "RSA")
	N string `json:"n"`
	E string `json:"e"`
	// EC fields (kty == "EC")
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// jwkKey wraps whatever crypto.PublicKey the raw JWK parsed to,
// plus the algorithm hint so verifySignature picks the right hash.
type jwkKey struct {
	pub crypto.PublicKey
	alg string
}

// parseJWK turns a raw JWK into a jwkKey — either *rsa.PublicKey
// or *ecdsa.PublicKey. Rejects malformed / unsupported keys.
func parseJWK(raw rawJWK) (jwkKey, error) {
	switch raw.Kty {
	case "RSA":
		n, err := base64.RawURLEncoding.DecodeString(raw.N)
		if err != nil {
			return jwkKey{}, fmt.Errorf("rsa n: %w", err)
		}
		e, err := base64.RawURLEncoding.DecodeString(raw.E)
		if err != nil {
			return jwkKey{}, fmt.Errorf("rsa e: %w", err)
		}
		if len(n) == 0 || len(e) == 0 {
			return jwkKey{}, errors.New("rsa key missing n or e")
		}
		return jwkKey{
			pub: &rsa.PublicKey{
				N: new(big.Int).SetBytes(n),
				E: int(new(big.Int).SetBytes(e).Int64()),
			},
			alg: raw.Alg,
		}, nil
	case "EC":
		crv, err := ecdsaCurveForName(raw.Crv)
		if err != nil {
			return jwkKey{}, err
		}
		x, err := base64.RawURLEncoding.DecodeString(raw.X)
		if err != nil {
			return jwkKey{}, fmt.Errorf("ec x: %w", err)
		}
		y, err := base64.RawURLEncoding.DecodeString(raw.Y)
		if err != nil {
			return jwkKey{}, fmt.Errorf("ec y: %w", err)
		}
		// Compose an SEC 1 uncompressed point (0x04 || X || Y) with
		// X, Y left-padded to the curve's byte size, then parse via
		// the stdlib. Preferred over building an ecdsa.PublicKey
		// literal (deprecated in Go 1.26).
		byteSize := (crv.Params().BitSize + 7) / 8
		if len(x) > byteSize || len(y) > byteSize {
			return jwkKey{}, fmt.Errorf("ec coordinate exceeds curve size (%d bytes)", byteSize)
		}
		uncompressed := make([]byte, 1+2*byteSize)
		uncompressed[0] = 0x04
		copy(uncompressed[1+byteSize-len(x):1+byteSize], x)
		copy(uncompressed[1+2*byteSize-len(y):], y)
		pub, err := ecdsa.ParseUncompressedPublicKey(crv, uncompressed)
		if err != nil {
			return jwkKey{}, fmt.Errorf("ec parse: %w", err)
		}
		return jwkKey{pub: pub, alg: raw.Alg}, nil
	default:
		return jwkKey{}, fmt.Errorf("unsupported kty %q", raw.Kty)
	}
}

// ecdsaCurveForName maps a JWK `crv` name to a stdlib elliptic
// curve. Restricted to what we announce as supportedAlgs.
func ecdsaCurveForName(name string) (elliptic.Curve, error) {
	switch name {
	case "P-256":
		return elliptic.P256(), nil
	case "P-384":
		return elliptic.P384(), nil
	default:
		return nil, fmt.Errorf("unsupported ec curve %q", name)
	}
}

// verifySignature dispatches on alg. Returns nil on valid
// signature, an error otherwise (never panics on wrong-key-type).
func verifySignature(alg string, key jwkKey, signed, sig []byte) error {
	switch alg {
	case "RS256":
		return verifyRSA(key, crypto.SHA256, signed, sig)
	case "RS384":
		return verifyRSA(key, crypto.SHA384, signed, sig)
	case "RS512":
		return verifyRSA(key, crypto.SHA512, signed, sig)
	case "ES256":
		return verifyECDSA(key, sha256.New(), signed, sig, 32)
	case "ES384":
		return verifyECDSA(key, sha512.New384(), signed, sig, 48)
	default:
		return fmt.Errorf("alg %q not supported", alg)
	}
}

func verifyRSA(key jwkKey, hash crypto.Hash, signed, sig []byte) error {
	pub, ok := key.pub.(*rsa.PublicKey)
	if !ok {
		return errors.New("rsa alg with non-RSA key")
	}
	h := hash.New()
	h.Write(signed)
	return rsa.VerifyPKCS1v15(pub, hash, h.Sum(nil), sig)
}

// verifyECDSA implements JWS ECDSA signature verification. The
// signature is the fixed-length concatenation of two big-endian
// integers r || s, each componentSize bytes long (RFC 7515 §3.4).
// Diff from OpenSSL's ASN.1 DER encoding — a common footgun for
// hand-rolled verifiers.
func verifyECDSA(key jwkKey, hasher hash.Hash, signed, sig []byte, componentSize int) error {
	pub, ok := key.pub.(*ecdsa.PublicKey)
	if !ok {
		return errors.New("ecdsa alg with non-ECDSA key")
	}
	if len(sig) != 2*componentSize {
		return fmt.Errorf("ecdsa signature length %d != %d", len(sig), 2*componentSize)
	}
	hasher.Write(signed)
	digest := hasher.Sum(nil)
	r := new(big.Int).SetBytes(sig[:componentSize])
	s := new(big.Int).SetBytes(sig[componentSize:])
	if !ecdsa.Verify(pub, digest, r, s) {
		return errors.New("ecdsa signature invalid")
	}
	return nil
}
