package license_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	fuzz "github.com/google/gofuzz"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/license"
	"github.com/sebastienrousseau/rousseau-agent/internal/testutil/fuzztest"
)

// TestFuzz_SignVerifyRoundtrip: the load-bearing property of the
// licensing subsystem — every valid Claims struct must round-trip
// through SignPayload → verifyToken (via Load) without loss.
//
// A regression here means a paying customer's licence could
// silently reject after a marshalling change; property-testing it
// gives us confidence the entire Claims schema is stable.
func TestFuzz_SignVerifyRoundtrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	// Install the pub key in the embedded keyring for the duration
	// of the test — same technique as the CLI doctor tests.
	orig := license.RawKeys
	t.Cleanup(func() { license.RawKeys = orig })
	license.RawKeys = base64StdEncode(pub)

	f := fuzztest.New(t).Funcs(
		// Tier: only "team" / "enterprise" are legal on a signed
		// token — the default string populator would generate
		// gibberish that Validate rejects.
		func(tier *license.Tier, c fuzz.Continue) {
			*tier = []license.Tier{license.TierTeam, license.TierEnterprise}[c.Intn(2)]
		},
		// Features: bound to the three real ones so effectiveFeatures
		// stays sane. Zero-length slices are legal (they mean "all
		// features in the tier").
		func(feats *[]license.Feature, c fuzz.Continue) {
			all := []license.Feature{license.FeatureSSO, license.FeatureAuditEgress, license.FeatureGovernanceAdvanced}
			n := c.Intn(len(all) + 1)
			out := make([]license.Feature, 0, n)
			for i := 0; i < n; i++ {
				out = append(out, all[c.Intn(len(all))])
			}
			*feats = out
		},
	)

	for i := 0; i < fuzztest.DefaultIterations; i++ {
		var claims license.Claims
		f.Fuzz(&claims)
		// Force a future expiry — the whole point is to exercise the
		// signature path, not the "expired at signing time" branch
		// which Validate correctly rejects.
		claims.ExpiresAt = time.Now().Add(90 * 24 * time.Hour).Unix()
		// Force a plausible IssuedAt (default int fuzzing produces
		// negative Unix seconds that Validate would flag).
		claims.IssuedAt = time.Now().Unix()

		tok, err := license.SignPayload(claims, priv)
		require.NoError(t, err, "SignPayload must accept every valid Claims")

		t.Setenv(license.DefaultEnvVar, tok)
		chk := license.Load(license.Source{}, nil)

		// The round-tripped tier must match — validates the JSON
		// wire format is stable under random inputs.
		require.Equal(t, claims.Tier, chk.Tier(), "tier round-trip")

		// Every feature the tier grants must report enabled.
		for _, feat := range expectedFeatures(claims) {
			require.True(t, chk.IsEnabled(feat),
				"feature %q on tier %q must be enabled", feat, claims.Tier)
		}
	}
}

// expectedFeatures mirrors the (unexported) effectiveFeatures
// method so the fuzz can assert against it without widening the
// license package's API. Kept in sync manually — if a new tier
// lands, both this helper and the production defaults change
// together.
func expectedFeatures(c license.Claims) []license.Feature {
	if len(c.Features) > 0 {
		out := make([]license.Feature, len(c.Features))
		copy(out, c.Features)
		return out
	}
	switch c.Tier {
	case license.TierTeam:
		return []license.Feature{license.FeatureSSO}
	case license.TierEnterprise:
		return []license.Feature{license.FeatureSSO, license.FeatureAuditEgress, license.FeatureGovernanceAdvanced}
	}
	return nil
}

// TestFuzz_TamperedTokenAlwaysRejects: for every valid signed
// token, flipping ANY byte of the signature MUST cause verification
// to fail. The Ed25519 property is well-known; the point of the
// fuzz is to catch a hypothetical future change that accepts a
// weakened signature (e.g. someone swaps in a truncated verifier).
func TestFuzz_TamperedTokenAlwaysRejects(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	orig := license.RawKeys
	t.Cleanup(func() { license.RawKeys = orig })
	license.RawKeys = base64StdEncode(pub)

	f := fuzztest.New(t)

	for i := 0; i < 100; i++ {
		claims := license.Claims{
			Subject:   "cust-fuzz",
			Tier:      license.TierEnterprise,
			ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
		}
		tok, err := license.SignPayload(claims, priv)
		require.NoError(t, err)

		// Decode the sig, flip a middle byte, re-encode. Flipping
		// a base64url character directly is unreliable at the
		// trailing edge because unused bits can map multiple chars
		// to the same decoded bytes. Middle-byte XOR is
		// deterministic.
		lastDot := strings.LastIndexByte(tok, '.')
		require.Greater(t, lastDot, 0)
		sig, err := base64.RawURLEncoding.DecodeString(tok[lastDot+1:])
		require.NoError(t, err)
		require.Greater(t, len(sig), 8)
		var flipIdx uint32
		f.Fuzz(&flipIdx) // uint32 avoids the negative-mod trap
		idx := int(flipIdx%uint32(len(sig)-2)) + 1
		sig[idx] ^= 0xFF
		mutated := tok[:lastDot+1] + base64.RawURLEncoding.EncodeToString(sig)

		t.Setenv(license.DefaultEnvVar, mutated)
		chk := license.Load(license.Source{}, nil)
		require.Equal(t, license.TierCore, chk.Tier(),
			"tampered token MUST fall back to core; iter=%d flipIdx=%d", i, flipIdx)
	}
}

// -- helpers -------------------------------------------------------

func base64StdEncode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
