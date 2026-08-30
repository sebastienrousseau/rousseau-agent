package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// silentLogger returns an slog.Logger that swallows output. Load()
// deliberately logs on every failure branch; the tests exercise
// those branches without polluting the test log.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestKeypair mints a fresh Ed25519 keypair per test. Never
// reused across tests — a leak from one test cannot forge a token
// another test relies on.
func newTestKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

// signTestToken wraps SignPayload for tests that don't care about
// the payload internals — they just want a valid-signature token
// with the given tier + expiry.
func signTestToken(t *testing.T, priv ed25519.PrivateKey, tier Tier, exp time.Time, features ...Feature) string {
	t.Helper()
	tok, err := SignPayload(Claims{
		Subject:   "cust-42",
		Tier:      tier,
		Features:  features,
		ExpiresAt: exp.Unix(),
	}, priv)
	require.NoError(t, err)
	return tok
}

func TestCore_ReportsAllFeaturesDisabled(t *testing.T) {
	// Every enterprise gate calls IsEnabled. Core MUST return false
	// for every feature, or the whole business model unravels — the
	// paid tier would silently activate on unlicensed installs.
	c := Core()
	assert.Equal(t, TierCore, c.Tier())
	for _, f := range []Feature{FeatureSSO, FeatureAuditEgress, FeatureGovernanceAdvanced} {
		assert.Falsef(t, c.IsEnabled(f), "core tier must not report %q as enabled", f)
	}
	info := c.Info()
	assert.Equal(t, TierCore, info.Tier)
	assert.False(t, info.Valid, "core Info.Valid must be false so operators can distinguish 'no license' from 'valid license'")
	assert.NotEmpty(t, info.Reason)
}

func TestClaims_ValidateRejectsBadTierAndMissingExpiry(t *testing.T) {
	now := time.Now()
	assert.Error(t, Claims{Tier: "unicorn", ExpiresAt: now.Add(time.Hour).Unix()}.Validate(now))
	assert.Error(t, Claims{Tier: TierEnterprise}.Validate(now), "missing expiry must not silently pass")
	assert.Error(t, Claims{Tier: TierEnterprise, ExpiresAt: now.Add(-time.Hour).Unix()}.Validate(now), "expired must fail")
	assert.NoError(t, Claims{Tier: TierEnterprise, ExpiresAt: now.Add(time.Hour).Unix()}.Validate(now))
	assert.NoError(t, Claims{Tier: TierTeam, ExpiresAt: now.Add(time.Hour).Unix()}.Validate(now))
}

func TestEffectiveFeatures_TierDefaults(t *testing.T) {
	// A license with no explicit Features list inherits the tier's
	// default set. Adding a new feature in the future automatically
	// unlocks it for the right tier without re-issuing existing
	// licenses.
	team := Claims{Tier: TierTeam}.effectiveFeatures()
	assert.Equal(t, []Feature{FeatureSSO}, team)

	ent := Claims{Tier: TierEnterprise}.effectiveFeatures()
	assert.ElementsMatch(t, []Feature{FeatureSSO, FeatureAuditEgress, FeatureGovernanceAdvanced}, ent)

	// Explicit list wins over tier defaults — supports "enterprise
	// customer minus one specific feature" contracts.
	explicit := Claims{Tier: TierEnterprise, Features: []Feature{FeatureSSO}}.effectiveFeatures()
	assert.Equal(t, []Feature{FeatureSSO}, explicit)

	// Core tier has no features (belt-and-braces — Core() short-
	// circuits before this path, but the fallback matters).
	core := Claims{Tier: TierCore}.effectiveFeatures()
	assert.Nil(t, core)
}

func TestSignPayload_RoundTripsThroughVerifyToken(t *testing.T) {
	// The signing / verifying pair MUST agree bit-for-bit — a
	// mismatch would mean rousseau's release infra ships tokens
	// deployed daemons refuse. This test lives inside the package
	// so drift is impossible.
	pub, priv := newTestKeypair(t)
	tok := signTestToken(t, priv, TierEnterprise, time.Now().Add(30*24*time.Hour))

	claims, err := verifyToken(tok, []ed25519.PublicKey{pub})
	require.NoError(t, err)
	assert.Equal(t, TierEnterprise, claims.Tier)
	assert.Equal(t, "cust-42", claims.Subject)
}

func TestVerifyToken_ForgeryRejected(t *testing.T) {
	// A token signed by an untrusted key MUST fail verification —
	// this is the whole point of the seam. Signing with keypair A
	// and verifying against keypair B's public half proves the
	// signature check isn't a stub.
	_, privAttacker := newTestKeypair(t)
	pubHonest, _ := newTestKeypair(t)

	forged := signTestToken(t, privAttacker, TierEnterprise, time.Now().Add(time.Hour))
	_, err := verifyToken(forged, []ed25519.PublicKey{pubHonest})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not verify")
}

func TestVerifyToken_MalformedInputs(t *testing.T) {
	pub, _ := newTestKeypair(t)
	cases := []struct {
		name, token string
	}{
		{"single-part", "onlyone"},
		{"three-parts", "a.b.c"},
		{"bad-base64-payload", "!!!.aaaa"},
		{"bad-base64-sig", "aaaa.!!!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := verifyToken(tc.token, []ed25519.PublicKey{pub})
			assert.Error(t, err)
		})
	}
}

func TestVerifyToken_NoKeysConfigured(t *testing.T) {
	// The OSS build ships with no keys. Any non-empty token must
	// then fail with a legible error rather than being silently
	// accepted — the "silently accepted" failure mode would
	// disable the paywall entirely.
	_, err := verifyToken("payload.sig", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no verification keys embedded")
}

func TestLoad_NoLicenseReturnsCore(t *testing.T) {
	// The vast-majority OSS install: no env var, no file. Load
	// returns Core; the daemon boots unchanged.
	t.Setenv("ROUSSEAU_LICENSE_KEY", "")
	c := Load(Source{}, silentLogger())
	assert.Equal(t, TierCore, c.Tier())
	assert.False(t, c.IsEnabled(FeatureSSO))
}

func TestLoad_EnvHappyPath(t *testing.T) {
	// Simulate a real deployment: mint a key locally, temporarily
	// swap RawKeys so EmbeddedPublicKeys returns it, then set the
	// env var to a token signed by that key.
	pub, priv := newTestKeypair(t)
	tok := signTestToken(t, priv, TierEnterprise, time.Now().Add(24*time.Hour))

	oldRaw := RawKeys
	RawKeys = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { RawKeys = oldRaw })

	t.Setenv("ROUSSEAU_LICENSE_KEY", tok)
	c := Load(Source{}, silentLogger())
	assert.Equal(t, TierEnterprise, c.Tier())
	assert.True(t, c.IsEnabled(FeatureSSO))
	assert.True(t, c.IsEnabled(FeatureAuditEgress))
	assert.True(t, c.IsEnabled(FeatureGovernanceAdvanced))

	info := c.Info()
	assert.True(t, info.Valid)
	assert.Equal(t, "cust-42", info.Subject)
}

func TestLoad_LongExpiryDoesNotFlagExpiring(t *testing.T) {
	// A license comfortably outside the 14-day warn window must
	// NOT set Expiring — otherwise every new sale would arrive
	// with a false "renew soon" flag.
	pub, priv := newTestKeypair(t)
	tok := signTestToken(t, priv, TierEnterprise, time.Now().Add(90*24*time.Hour))

	oldRaw := RawKeys
	RawKeys = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { RawKeys = oldRaw })

	t.Setenv("ROUSSEAU_LICENSE_KEY", tok)
	info := Load(Source{}, silentLogger()).Info()
	assert.True(t, info.Valid)
	assert.False(t, info.Expiring, "90 days is well outside the 14-day warn window")
}

func TestLoad_ExpiringLicenseFlagsWarnWindow(t *testing.T) {
	// A license within the warn window is still valid — the daemon
	// should still activate its features — but Info.Expiring flips
	// on so the doctor command can surface a renewal reminder.
	pub, priv := newTestKeypair(t)
	tok := signTestToken(t, priv, TierEnterprise, time.Now().Add(time.Hour))

	oldRaw := RawKeys
	RawKeys = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { RawKeys = oldRaw })

	t.Setenv("ROUSSEAU_LICENSE_KEY", tok)
	c := Load(Source{}, silentLogger())
	info := c.Info()
	require.True(t, info.Valid)
	assert.True(t, info.Expiring)
}

func TestLoad_ExpiredLicenseFallsBackToCore(t *testing.T) {
	// Expired = same visible outcome as no license: features off.
	// But Info.Reason must name the failure so operators debug it
	// quickly.
	pub, priv := newTestKeypair(t)
	tok := signTestToken(t, priv, TierEnterprise, time.Now().Add(-time.Hour))

	oldRaw := RawKeys
	RawKeys = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { RawKeys = oldRaw })

	t.Setenv("ROUSSEAU_LICENSE_KEY", tok)
	c := Load(Source{}, silentLogger())
	assert.Equal(t, TierCore, c.Tier())
	assert.False(t, c.IsEnabled(FeatureSSO))
	assert.Contains(t, c.Info().Reason, "expired")
}

func TestLoad_BadSignatureFallsBackToCoreWithReason(t *testing.T) {
	// Token was signed with an untrusted key. Load MUST NOT
	// silently accept — falls back to core with a legible reason.
	_, priv := newTestKeypair(t)
	pubTrusted, _ := newTestKeypair(t)

	tok := signTestToken(t, priv, TierEnterprise, time.Now().Add(time.Hour))
	oldRaw := RawKeys
	RawKeys = base64.StdEncoding.EncodeToString(pubTrusted)
	t.Cleanup(func() { RawKeys = oldRaw })

	t.Setenv("ROUSSEAU_LICENSE_KEY", tok)
	c := Load(Source{}, silentLogger())
	assert.Equal(t, TierCore, c.Tier())
	assert.Contains(t, c.Info().Reason, "signature")
}

func TestLoad_FileSourceHonoursMode0600Only(t *testing.T) {
	// The license file must be mode 0600 — anything more permissive
	// on a shared box leaks the key. Rejection returns core; the
	// daemon boots but the paid features stay off with a legible
	// reason.
	dir := t.TempDir()
	path := filepath.Join(dir, "license.key")
	require.NoError(t, os.WriteFile(path, []byte("payload.sig"), 0o644)) // too permissive

	c := Load(Source{File: path, Env: "-"}, silentLogger())
	assert.Equal(t, TierCore, c.Tier())
	assert.Contains(t, c.Info().Reason, "permissive mode")
}

func TestLoad_FileSourceHappyPath(t *testing.T) {
	pub, priv := newTestKeypair(t)
	tok := signTestToken(t, priv, TierTeam, time.Now().Add(24*time.Hour))

	oldRaw := RawKeys
	RawKeys = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { RawKeys = oldRaw })

	dir := t.TempDir()
	path := filepath.Join(dir, "license.key")
	require.NoError(t, os.WriteFile(path, []byte(tok), 0o600))

	c := Load(Source{File: path, Env: "-"}, silentLogger())
	assert.Equal(t, TierTeam, c.Tier())
	assert.True(t, c.IsEnabled(FeatureSSO))
	assert.False(t, c.IsEnabled(FeatureAuditEgress), "team tier does not include audit egress — enterprise-only")
}

func TestLoad_EnvBeatsFileWhenBothSet(t *testing.T) {
	// Explicit priority documented on Source: env wins over file
	// when both are set. Injecting a token via env is the standard
	// container/systemd pattern; the file is the fallback.
	pub, priv := newTestKeypair(t)
	envTok := signTestToken(t, priv, TierEnterprise, time.Now().Add(24*time.Hour))
	fileTok := signTestToken(t, priv, TierTeam, time.Now().Add(24*time.Hour))

	oldRaw := RawKeys
	RawKeys = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { RawKeys = oldRaw })

	dir := t.TempDir()
	path := filepath.Join(dir, "license.key")
	require.NoError(t, os.WriteFile(path, []byte(fileTok), 0o600))

	t.Setenv("ROUSSEAU_LICENSE_KEY", envTok)
	c := Load(Source{File: path}, silentLogger())
	assert.Equal(t, TierEnterprise, c.Tier(), "env must win when both env and file provide a token")
}

func TestLoad_EnvDisabledViaDashSentinel(t *testing.T) {
	// Setting Source.Env = "-" disables env lookup entirely, so a
	// container that inherits ROUSSEAU_LICENSE_KEY from its parent
	// shell doesn't accidentally use it. File-only mode.
	t.Setenv("ROUSSEAU_LICENSE_KEY", "some-poisoned-value")
	c := Load(Source{Env: "-"}, silentLogger())
	// No file → no license → core.
	assert.Equal(t, TierCore, c.Tier())
	// The "some-poisoned-value" from env MUST NOT show up as a
	// verification failure — env was disabled entirely.
	assert.Contains(t, c.Info().Reason, "no license configured")
}

func TestEmbeddedPublicKeys_EmptyByDefault(t *testing.T) {
	// The shipped OSS build has RawKeys="". Enterprise builds inject
	// via -ldflags. The default MUST be empty — a stray key in the
	// OSS binary is a supply-chain incident.
	assert.Empty(t, RawKeys, "RawKeys must be empty in the OSS default; -ldflags injects it for enterprise builds")
	assert.Empty(t, EmbeddedPublicKeys())
}

func TestEmbeddedPublicKeys_ParsesMultipleAndSkipsGarbage(t *testing.T) {
	// Key rotation: two valid keys plus a garbage entry.
	// EmbeddedPublicKeys must return the two valid keys and skip
	// the junk (a typo in the ldflag mustn't take the whole
	// keyring offline).
	pub1, _ := newTestKeypair(t)
	pub2, _ := newTestKeypair(t)
	oldRaw := RawKeys
	RawKeys = base64.StdEncoding.EncodeToString(pub1) +
		",not-base64-!!!," +
		base64.StdEncoding.EncodeToString(pub2) +
		"," + base64.StdEncoding.EncodeToString([]byte("too-short"))
	t.Cleanup(func() { RawKeys = oldRaw })

	got := EmbeddedPublicKeys()
	require.Len(t, got, 2)
	assert.Equal(t, []byte(pub1), []byte(got[0]))
	assert.Equal(t, []byte(pub2), []byte(got[1]))
}

func TestLoad_FileAbsentIsSilentCoreFallback(t *testing.T) {
	// A configured file path that doesn't exist yet must NOT crash
	// or noisily fail — the operator may have set the path in
	// anticipation of a delivery. Returns core with no reason
	// beyond the standard "no license configured" default.
	c := Load(Source{File: "/tmp/definitely-does-not-exist-" + t.Name(), Env: "-"}, silentLogger())
	assert.Equal(t, TierCore, c.Tier())
}

func TestReadToken_StatErrorSurfaces(t *testing.T) {
	// Pointing at a path that stat can't reach (a component of the
	// path isn't a directory) surfaces the stat error legibly. On
	// this path IsNotExist is false, so the "other stat error"
	// branch fires.
	dir := t.TempDir()
	// Create a regular file, then use it as a "parent directory".
	// Stat on "/tmp/.../file/x" returns ENOTDIR, not IsNotExist.
	notADir := filepath.Join(dir, "regular-file")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))
	c := Load(Source{File: filepath.Join(notADir, "x"), Env: "-"}, silentLogger())
	assert.Equal(t, TierCore, c.Tier())
	assert.Contains(t, c.Info().Reason, "stat")
}

func TestReadToken_UnreadableFileSurfaces(t *testing.T) {
	// A file with mode 0000 fails at os.ReadFile (not at Stat).
	// Rare in practice but covers the read-error branch.
	if os.Geteuid() == 0 {
		t.Skip("running as root — unreadable-file test cannot fail as expected")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "no-read")
	require.NoError(t, os.WriteFile(path, []byte("payload.sig"), 0o600))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Logf("cleanup chmod: %v", err)
		}
	})

	c := Load(Source{File: path, Env: "-"}, silentLogger())
	assert.Equal(t, TierCore, c.Tier())
	// The failure surfaces as either a "permissive mode" reject
	// (0o000 & 0o077 == 0 so stat wouldn't reject it) or a read
	// error — both are acceptable and both fall back to core.
	assert.NotEmpty(t, c.Info().Reason)
}

func TestLoad_NilLoggerIsSafe(t *testing.T) {
	// Callers may pass a nil logger — Load defaults to
	// slog.Default(). Test the branch that exists to avoid a
	// nil-deref on the log-line paths.
	t.Setenv("ROUSSEAU_LICENSE_KEY", "")
	c := Load(Source{}, nil)
	assert.Equal(t, TierCore, c.Tier())
}

func TestSignPayload_UsesProvidedIssuedAt(t *testing.T) {
	// A caller who supplies IssuedAt (e.g. deterministic reissue
	// for a rotation test) sees exactly that timestamp on the
	// wire, not now(). Distinct from the default branch that
	// stamps IssuedAt = time.Now().
	_, priv := newTestKeypair(t)
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	tok, err := SignPayload(Claims{
		Subject:   "cust-A",
		Tier:      TierEnterprise,
		IssuedAt:  fixed,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, priv)
	require.NoError(t, err)

	pub := priv.Public().(ed25519.PublicKey)
	claims, err := verifyToken(tok, []ed25519.PublicKey{pub})
	require.NoError(t, err)
	assert.Equal(t, fixed, claims.IssuedAt, "SignPayload must NOT overwrite a caller-supplied IssuedAt")
}

func TestVerifyToken_ValidSignatureOnNonJSONPayloadRejected(t *testing.T) {
	// A payload that verifies (the attacker holds a trusted key —
	// e.g. via HSM misconfiguration on the issuer side) but isn't
	// valid JSON must fail at the unmarshal step with a legible
	// error. This closes the "signed garbage" branch of verifyToken.
	pub, priv := newTestKeypair(t)

	// Manually construct a token with a validly-signed but non-JSON
	// payload. Reuses the same wire format as SignPayload.
	payload := []byte("this-is-not-json")
	sig := ed25519.Sign(priv, payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)

	_, err := verifyToken(token, []ed25519.PublicKey{pub})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "payload json")
}

func TestEmbeddedPublicKeys_SkipsEmptyEntries(t *testing.T) {
	// A comma-happy operator (or a ldflag with trailing commas)
	// must not blow up the keyring. `"key,,key2"` produces the
	// two keys and skips the empty in the middle.
	pub1, _ := newTestKeypair(t)
	oldRaw := RawKeys
	RawKeys = "," + base64.StdEncoding.EncodeToString(pub1) + ",,"
	t.Cleanup(func() { RawKeys = oldRaw })

	got := EmbeddedPublicKeys()
	require.Len(t, got, 1)
	assert.Equal(t, []byte(pub1), []byte(got[0]))
}

func TestInfo_NeverExposesRawToken(t *testing.T) {
	// The Info snapshot goes to logs + the doctor command. It must
	// never include the raw signed token — that would leak the
	// customer's key into log aggregators. This test is a shape
	// assertion: enumerate the fields, none should be the token
	// (the token is not stored anywhere in the Info struct by
	// design).
	pub, priv := newTestKeypair(t)
	tok := signTestToken(t, priv, TierEnterprise, time.Now().Add(time.Hour))
	oldRaw := RawKeys
	RawKeys = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { RawKeys = oldRaw })

	t.Setenv("ROUSSEAU_LICENSE_KEY", tok)
	info := Load(Source{}, silentLogger()).Info()

	// Every string field must not contain any 20+ char run from
	// the token (an approximation of "did the token leak?").
	sample := tok[:min(30, len(tok))]
	assert.NotContains(t, info.Subject, sample)
	assert.NotContains(t, info.Reason, sample)
}
