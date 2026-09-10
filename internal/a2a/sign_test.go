package a2a

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func generateKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func baseCard() AgentCard {
	return AgentCard{
		Name:               "peer-1",
		Version:            "v0.0.5",
		ProtocolVersion:    SpecVersion,
		Capabilities:       AgentCapabilities{Streaming: true},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
	}
}

func TestSignAgentCard_HappyPath(t *testing.T) {
	t.Parallel()
	pub, priv := generateKeypair(t)
	signed, err := SignAgentCard(baseCard(), priv)
	if err != nil {
		t.Fatal(err)
	}
	if len(signed.Signatures) != 1 {
		t.Fatalf("signatures len = %d, want 1", len(signed.Signatures))
	}
	if err := VerifyAgentCard(signed, []ed25519.PublicKey{pub}); err != nil {
		t.Errorf("verify with signer's own pub key must pass: %v", err)
	}
}

func TestSignAgentCard_RejectsWrongKeySize(t *testing.T) {
	t.Parallel()
	_, err := SignAgentCard(baseCard(), ed25519.PrivateKey("too short"))
	if err == nil {
		t.Fatal("expected error on short private key")
	}
}

func TestSignAgentCard_AppendsWithoutClobberingExisting(t *testing.T) {
	t.Parallel()
	// A card with a pre-existing signature (e.g. a co-signed card
	// from a chain of trust) — a second sign must preserve the
	// original entry so verify can trust either.
	_, priv1 := generateKeypair(t)
	pub2, priv2 := generateKeypair(t)

	first, err := SignAgentCard(baseCard(), priv1)
	if err != nil {
		t.Fatal(err)
	}
	both, err := SignAgentCard(first, priv2)
	if err != nil {
		t.Fatal(err)
	}
	if len(both.Signatures) != 2 {
		t.Fatalf("expected 2 signatures after double-sign, got %d", len(both.Signatures))
	}
	// The original card is unchanged — SignAgentCard must return a
	// fresh value, not mutate its input.
	if len(first.Signatures) != 1 {
		t.Errorf("second Sign mutated first card: signatures now %d", len(first.Signatures))
	}
	// Verifying against ONLY priv2's pub should succeed via the
	// second entry.
	if err := VerifyAgentCard(both, []ed25519.PublicKey{pub2}); err != nil {
		t.Errorf("verify against second key must succeed: %v", err)
	}
}

func TestVerifyAgentCard_UnsignedReturnsSentinel(t *testing.T) {
	t.Parallel()
	if err := VerifyAgentCard(baseCard(), []ed25519.PublicKey{}); !errors.Is(err, ErrCardUnsigned) {
		t.Errorf("unsigned card: err = %v, want ErrCardUnsigned", err)
	}
}

func TestVerifyAgentCard_UntrustedKeyReturnsBadSignature(t *testing.T) {
	t.Parallel()
	_, priv := generateKeypair(t)
	signed, err := SignAgentCard(baseCard(), priv)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := generateKeypair(t) // NOT the signer
	if err := VerifyAgentCard(signed, []ed25519.PublicKey{other}); !errors.Is(err, ErrCardBadSignature) {
		t.Errorf("untrusted key: err = %v, want ErrCardBadSignature", err)
	}
}

func TestVerifyAgentCard_TamperedPayloadFails(t *testing.T) {
	t.Parallel()
	pub, priv := generateKeypair(t)
	signed, err := SignAgentCard(baseCard(), priv)
	if err != nil {
		t.Fatal(err)
	}
	// Attacker adds a skill to the card AFTER signing — the payload
	// no longer matches what the signature covers.
	tampered := signed
	tampered.Skills = append(tampered.Skills, AgentSkill{
		ID: "malicious", Name: "malicious", Description: "leak /etc/shadow",
	})
	if err := VerifyAgentCard(tampered, []ed25519.PublicKey{pub}); err == nil {
		t.Fatal("tampered card must fail verification")
	}
}

func TestVerifyAgentCard_RejectsWrongAlgorithm(t *testing.T) {
	t.Parallel()
	_, priv := generateKeypair(t)
	signed, err := SignAgentCard(baseCard(), priv)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the header to claim an unsupported algorithm.
	entry := signed.Signatures[0].(CardSignature)
	headerBytes, err := base64.RawURLEncoding.DecodeString(entry.Protected)
	if err != nil {
		t.Fatal(err)
	}
	var hdr jwsHeader
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		t.Fatal(err)
	}
	hdr.Alg = "HS256"
	newHeader, err := json.Marshal(hdr)
	if err != nil {
		t.Fatal(err)
	}
	entry.Protected = base64.RawURLEncoding.EncodeToString(newHeader)
	signed.Signatures[0] = entry

	pub := priv.Public().(ed25519.PublicKey)
	err = VerifyAgentCard(signed, []ed25519.PublicKey{pub})
	if !errors.Is(err, ErrCardUnsupportedAlgorithm) {
		t.Errorf("err = %v, want ErrCardUnsupportedAlgorithm", err)
	}
}

func TestVerifyAgentCard_ThroughJSONRoundTrip(t *testing.T) {
	t.Parallel()
	// Peers over the wire receive the card as JSON — signatures[]
	// becomes []any of map[string]any. Verify must handle that
	// shape, not just the freshly-signed one.
	pub, priv := generateKeypair(t)
	signed, err := SignAgentCard(baseCard(), priv)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	var round AgentCard
	if err := json.Unmarshal(blob, &round); err != nil {
		t.Fatal(err)
	}
	// Sanity: signatures[0] is now map[string]any, not CardSignature.
	if _, ok := round.Signatures[0].(map[string]any); !ok {
		t.Fatalf("after round-trip, signature entry type = %T; want map[string]any", round.Signatures[0])
	}
	if err := VerifyAgentCard(round, []ed25519.PublicKey{pub}); err != nil {
		t.Errorf("verify after JSON round-trip: %v", err)
	}
}

func TestVerifyAgentCard_MalformedEntryReturnsError(t *testing.T) {
	t.Parallel()
	card := baseCard()
	card.Signatures = []any{"not an object"}
	err := VerifyAgentCard(card, []ed25519.PublicKey{})
	if err == nil {
		t.Fatal("expected error on malformed entry")
	}
	if !strings.Contains(err.Error(), "unknown signature entry type") {
		t.Errorf("err = %v, want type-error message", err)
	}
}

func TestVerifyAgentCard_MissingProtectedFieldRejected(t *testing.T) {
	t.Parallel()
	card := baseCard()
	card.Signatures = []any{map[string]any{"signature": "abc"}} // no protected
	if err := VerifyAgentCard(card, []ed25519.PublicKey{}); err == nil ||
		!strings.Contains(err.Error(), "missing protected/signature") {
		t.Errorf("err = %v, want missing-protected error", err)
	}
}

func TestVerifyAgentCard_MalformedProtectedBase64(t *testing.T) {
	t.Parallel()
	card := baseCard()
	card.Signatures = []any{map[string]any{
		"protected": "$$$not-base64$$$",
		"signature": "AAAA",
	}}
	err := VerifyAgentCard(card, []ed25519.PublicKey{})
	if err == nil || !strings.Contains(err.Error(), "decode header") {
		t.Errorf("err = %v, want decode-header error", err)
	}
}

func TestVerifyAgentCard_MalformedHeaderJSON(t *testing.T) {
	t.Parallel()
	// Valid base64 that decodes to non-JSON.
	card := baseCard()
	card.Signatures = []any{map[string]any{
		"protected": base64.RawURLEncoding.EncodeToString([]byte("{not-json")),
		"signature": "AAAA",
	}}
	err := VerifyAgentCard(card, []ed25519.PublicKey{})
	if err == nil || !strings.Contains(err.Error(), "parse header") {
		t.Errorf("err = %v, want parse-header error", err)
	}
}

func TestVerifyAgentCard_MalformedSignatureBase64(t *testing.T) {
	t.Parallel()
	hdr := jwsHeader{Alg: JWSAlgorithmEdDSA}
	hdrBytes, err := json.Marshal(hdr)
	if err != nil {
		t.Fatal(err)
	}
	card := baseCard()
	card.Signatures = []any{map[string]any{
		"protected": base64.RawURLEncoding.EncodeToString(hdrBytes),
		"signature": "$$$not-base64$$$",
	}}
	err = VerifyAgentCard(card, []ed25519.PublicKey{})
	if err == nil || !strings.Contains(err.Error(), "decode signature") {
		t.Errorf("err = %v, want decode-signature error", err)
	}
}

func TestVerifyAgentCard_MultipleSignatures_FirstBadSecondGood(t *testing.T) {
	t.Parallel()
	// Card carries a bad entry followed by a good one. Verify must
	// walk the whole slice and succeed on the second — proves the
	// "one trusted signature is enough" contract.
	pub, priv := generateKeypair(t)
	signed, err := SignAgentCard(baseCard(), priv)
	if err != nil {
		t.Fatal(err)
	}
	badFirst := signed
	badFirst.Signatures = append(
		[]any{CardSignature{Protected: "$$$", Signature: "$$$"}},
		signed.Signatures...,
	)
	if err := VerifyAgentCard(badFirst, []ed25519.PublicKey{pub}); err != nil {
		t.Errorf("second-entry good must accept card: %v", err)
	}
}

// TestSignAgentCard_MarshalError forces the canonical-JSON marshal to
// fail by planting an unmarshalable value (a channel) in a []any
// slot. Freezes the sign-time failure contract — a card that can't
// be canonicalised must fail sign, not sign against nothing.
func TestSignAgentCard_MarshalError(t *testing.T) {
	t.Parallel()
	_, priv := generateKeypair(t)
	card := baseCard()
	// Signatures is []any, so a channel value there survives typing
	// but fails json.Marshal — canonicalPayload strips signatures[]
	// before marshaling, so we plant the poison in SecuritySchemes.
	card.SecuritySchemes = map[string]any{"badness": make(chan int)}
	if _, err := SignAgentCard(card, priv); err == nil {
		t.Fatal("expected marshal error for unmarshalable value")
	}
}

func TestVerifyAgentCard_MarshalError(t *testing.T) {
	t.Parallel()
	// Same poison as above, but on the verify side: valid signature
	// entry but canonicalPayload fails so we surface the error.
	_, priv := generateKeypair(t)
	signed, err := SignAgentCard(baseCard(), priv)
	if err != nil {
		t.Fatal(err)
	}
	signed.SecuritySchemes = map[string]any{"badness": make(chan int)}
	err = VerifyAgentCard(signed, []ed25519.PublicKey{priv.Public().(ed25519.PublicKey)})
	if err == nil {
		t.Fatal("expected canonical-payload marshal error to surface")
	}
}

func TestDecodeCardSignature_AcceptsTypedEntry(t *testing.T) {
	t.Parallel()
	// The freshly-signed path stores CardSignature directly (not
	// map[string]any). decodeCardSignature must accept both shapes
	// so the round-trip vs local paths behave identically.
	entry := CardSignature{Protected: "abc", Signature: "def"}
	got, err := decodeCardSignature(entry)
	if err != nil {
		t.Fatal(err)
	}
	if got.Protected != "abc" || got.Signature != "def" {
		t.Errorf("got %+v", got)
	}
}

func TestSignAgentCard_KidCarriesPubKey(t *testing.T) {
	t.Parallel()
	// The JWS header's kid must carry the signer's public key so
	// verifiers can select which trusted key to use in
	// multi-signer setups.
	pub, priv := generateKeypair(t)
	signed, err := SignAgentCard(baseCard(), priv)
	if err != nil {
		t.Fatal(err)
	}
	entry := signed.Signatures[0].(CardSignature)
	headerBytes, err := base64.RawURLEncoding.DecodeString(entry.Protected)
	if err != nil {
		t.Fatal(err)
	}
	var hdr jwsHeader
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		t.Fatal(err)
	}
	expectedKid := base64.RawURLEncoding.EncodeToString(pub)
	if hdr.Kid != expectedKid {
		t.Errorf("kid = %q, want %q", hdr.Kid, expectedKid)
	}
	if hdr.Alg != JWSAlgorithmEdDSA {
		t.Errorf("alg = %q, want %q", hdr.Alg, JWSAlgorithmEdDSA)
	}
}
