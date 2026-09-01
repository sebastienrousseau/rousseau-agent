package sso

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/testutil/fuzztest"
)

// TestFuzz_AudienceUnmarshalStringOrArray: the OIDC `aud` claim
// is legally a string OR a string array (RFC 7519 §4.1.3). The
// custom UnmarshalJSON on the audience type MUST accept both
// shapes without loss for every random value.
func TestFuzz_AudienceUnmarshalStringOrArray(t *testing.T) {
	f := fuzztest.New(t)

	// String shape.
	for i := 0; i < 100; i++ {
		var v string
		f.Fuzz(&v)
		b, err := json.Marshal(v)
		require.NoError(t, err)
		var got audience
		require.NoError(t, got.UnmarshalJSON(b))
		require.Len(t, got, 1)
		assert.Equal(t, v, got[0])
	}

	// Array shape — vary length + members.
	for i := 0; i < 100; i++ {
		var arr []string
		f.Fuzz(&arr)
		b, err := json.Marshal(arr)
		require.NoError(t, err)
		var got audience
		require.NoError(t, got.UnmarshalJSON(b))
		require.Equal(t, len(arr), len(got))
		for j := range arr {
			assert.Equal(t, arr[j], got[j])
		}
	}
}

// TestFuzz_AudienceRejectsWrongShapes: for every random primitive
// that isn't a string or []string, UnmarshalJSON MUST return an
// error. Property: no silent acceptance of malformed aud claims
// (would open a cross-tenant token replay).
func TestFuzz_AudienceRejectsWrongShapes(t *testing.T) {
	f := fuzztest.New(t)

	// Objects.
	for i := 0; i < 50; i++ {
		obj := map[string]int{}
		f.Fuzz(&obj)
		b, err := json.Marshal(obj)
		require.NoError(t, err)
		var got audience
		assert.Error(t, got.UnmarshalJSON(b), "object aud must be rejected")
	}

	// Numbers.
	for i := 0; i < 50; i++ {
		var n int
		f.Fuzz(&n)
		b, err := json.Marshal(n)
		require.NoError(t, err)
		var got audience
		assert.Error(t, got.UnmarshalJSON(b), "number aud must be rejected")
	}
}

// TestFuzz_IdentityJSONRoundtrip: Identity should survive a full
// JSON round-trip so audit-log egress + downstream consumers
// don't lose fields under random inputs. Property: no
// marshaling-time silent field drop.
func TestFuzz_IdentityJSONRoundtrip(t *testing.T) {
	f := fuzztest.New(t)
	for i := 0; i < fuzztest.DefaultIterations; i++ {
		var id Identity
		f.Fuzz(&id)
		b, err := json.Marshal(id)
		require.NoError(t, err)
		var back Identity
		require.NoError(t, json.Unmarshal(b, &back))
		assert.Equal(t, id.Subject, back.Subject)
		assert.Equal(t, id.Email, back.Email)
		assert.Equal(t, id.EmailVerified, back.EmailVerified)
		assert.Equal(t, id.DisplayName, back.DisplayName)
		assert.Equal(t, len(id.Groups), len(back.Groups))
		assert.Equal(t, len(id.TransportIDs), len(back.TransportIDs))
		assert.True(t, id.ExpiresAt.Equal(back.ExpiresAt))
	}
}

// TestFuzz_NopVerifyTokenAlwaysErrs: the Nop directory must
// reject every input regardless of shape — a sink returning nil
// on a random token would let unlicensed operators through the
// gate silently.
func TestFuzz_NopVerifyTokenAlwaysErrs(t *testing.T) {
	f := fuzztest.New(t)
	n := Nop{}
	for i := 0; i < 200; i++ {
		var tok string
		f.Fuzz(&tok)
		_, err := n.VerifyToken(t.Context(), tok)
		require.ErrorIs(t, err, ErrDirectoryDisabled)
	}
}

// TestFuzz_MalformedTokensNeverPanic: no random 3-part-ish string
// may panic the JWT verifier. Property: the parser is
// crash-free on adversarial inputs (belt-and-braces alongside
// the existing table-driven malformed-token tests).
func TestFuzz_MalformedTokensNeverPanic(t *testing.T) {
	// The verifier needs a Directory — construct a tolerant one
	// pointing at a nonexistent issuer. Bad discovery is exactly
	// what a random token would fail on anyway.
	d, err := NewOIDCDirectory(OIDCConfig{Issuer: "http://127.0.0.1:1"}, nil)
	require.NoError(t, err)

	f := fuzztest.New(t)
	for i := 0; i < 200; i++ {
		var a, b, c string
		f.Fuzz(&a)
		f.Fuzz(&b)
		f.Fuzz(&c)
		tok := fmt.Sprintf("%s.%s.%s", a, b, c)
		require.NotPanics(t, func() {
			_, verifyErr := d.VerifyToken(t.Context(), tok)
			// Errors are the whole point of this fuzz — we only
			// care that the call doesn't panic. Reference the
			// error to satisfy errcheck.
			_ = verifyErr
		})
	}
}
