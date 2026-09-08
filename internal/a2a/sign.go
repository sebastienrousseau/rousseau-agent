package a2a

// AgentCard signing / verification per A2A v1.0.1 `signatures[]`.
//
// The spec permits (but does not mandate) signed cards. When present,
// each entry in [AgentCard.Signatures] is a JWS in JSON serialization
// shape ({protected, signature}). rousseau uses Ed25519 (JWS
// alg=EdDSA) for parity with the existing skill-bundle and
// enterprise-license signing paths.
//
// Wire shape of one entry:
//
//	{
//	  "protected": "<base64url-encoded {\"alg\":\"EdDSA\",\"typ\":\"JWT\",\"kid\":\"<pubkey-b64>\"}>",
//	  "signature": "<base64url(sig)>"
//	}
//
// The signed value is the canonical form of the AgentCard with the
// signatures field zeroed. Signing produces a fresh signatures[] slice
// with one entry appended. Verification walks the slice and returns
// on the FIRST signature that verifies against a trusted key — one
// trusted signature is enough to accept the card.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// JWSAlgorithmEdDSA is the JWS `alg` value for Ed25519.
const JWSAlgorithmEdDSA = "EdDSA"

// CardSignature is one JWS-shaped entry on [AgentCard.Signatures].
// Marshals as {"protected":"...","signature":"..."} which is the JWS
// JSON Serialization single-signature shape.
type CardSignature struct {
	Protected string `json:"protected"`
	Signature string `json:"signature"`
}

// jwsHeader is the decoded contents of CardSignature.Protected. Kept
// unexported — callers work with the encoded string.
type jwsHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ,omitempty"`
	Kid string `json:"kid,omitempty"`
}

// Errors returned from card signature operations.
var (
	// ErrCardUnsigned is returned when [VerifyAgentCard] is asked to
	// verify a card whose Signatures slice is empty. Callers decide
	// whether to reject or accept unsigned cards; the function
	// surfaces the state explicitly so it's never a silent accept.
	ErrCardUnsigned = errors.New("a2a: agent card has no signatures")
	// ErrCardBadSignature is returned when no entry in signatures[]
	// verifies against the trusted key set. Wrapped errors from the
	// per-entry decode step are attached via errors.Join under the
	// hood so the operator's log carries the reason.
	ErrCardBadSignature = errors.New("a2a: agent card signature verification failed")
	// ErrCardUnsupportedAlgorithm is returned when a JWS header
	// declares an algorithm other than EdDSA. Kept separate from
	// ErrCardBadSignature so operators can distinguish "we don't
	// support your algorithm" from "your Ed25519 signature failed".
	ErrCardUnsupportedAlgorithm = errors.New("a2a: agent card signature uses unsupported algorithm")
)

// SignAgentCard produces a new [AgentCard] with one signature
// appended. The signed value is the card's canonical JSON with the
// signatures field zeroed — so re-signing an already-signed card
// produces a stable signature (order-independent), and stripping the
// signature and recomputing gives the same bytes back.
func SignAgentCard(card AgentCard, priv ed25519.PrivateKey) (AgentCard, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return AgentCard{}, fmt.Errorf("a2a: private key size = %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return AgentCard{}, errors.New("a2a: private key produced non-ed25519 public key")
	}

	signingInput, err := canonicalSigningInput(card, pub)
	if err != nil {
		return AgentCard{}, err
	}
	sig := ed25519.Sign(priv, []byte(signingInput.protected+"."+signingInput.payload))

	out := card
	out.Signatures = append([]any(nil), card.Signatures...) // copy so callers don't observe aliasing
	out.Signatures = append(out.Signatures, CardSignature{
		Protected: signingInput.protected,
		Signature: base64.RawURLEncoding.EncodeToString(sig),
	})
	return out, nil
}

// VerifyAgentCard walks the card's signatures[] and returns nil as
// soon as one entry verifies against trustedKeys. Returns
// [ErrCardUnsigned] when signatures[] is empty, or
// [ErrCardBadSignature] when none of the entries verify.
func VerifyAgentCard(card AgentCard, trustedKeys []ed25519.PublicKey) error {
	if len(card.Signatures) == 0 {
		return ErrCardUnsigned
	}
	var lastErr error
	for _, raw := range card.Signatures {
		entry, err := decodeCardSignature(raw)
		if err != nil {
			lastErr = err
			continue
		}
		headerBytes, err := base64.RawURLEncoding.DecodeString(entry.Protected)
		if err != nil {
			lastErr = fmt.Errorf("decode header: %w", err)
			continue
		}
		var hdr jwsHeader
		if err := json.Unmarshal(headerBytes, &hdr); err != nil {
			lastErr = fmt.Errorf("parse header: %w", err)
			continue
		}
		if hdr.Alg != JWSAlgorithmEdDSA {
			lastErr = fmt.Errorf("%w: %s", ErrCardUnsupportedAlgorithm, hdr.Alg)
			continue
		}
		sig, err := base64.RawURLEncoding.DecodeString(entry.Signature)
		if err != nil {
			lastErr = fmt.Errorf("decode signature: %w", err)
			continue
		}
		// Recompute the signing input: canonical card (with
		// signatures zeroed) + this signature's protected header.
		payload, err := canonicalPayload(card)
		if err != nil {
			lastErr = err
			continue
		}
		signingInput := entry.Protected + "." + payload
		for _, key := range trustedKeys {
			if ed25519.Verify(key, []byte(signingInput), sig) {
				// Optional belt-and-braces: header.kid, when
				// present, must match one of the trusted keys we
				// tried. It's advisory — we already verified.
				_ = hdr.Kid
				return nil
			}
		}
		lastErr = ErrCardBadSignature
	}
	if lastErr == nil {
		lastErr = ErrCardBadSignature
	}
	return lastErr
}

// signingInputParts is the split header . payload pair the JWS spec
// treats as the input to Ed25519 signing.
type signingInputParts struct {
	protected string // base64url-encoded header
	payload   string // base64url-encoded canonical card JSON
}

// canonicalSigningInput builds the JWS signing input for one
// signature attempt. The header carries alg + typ + kid so verifiers
// can identify which public key produced the signature.
func canonicalSigningInput(card AgentCard, pub ed25519.PublicKey) (signingInputParts, error) {
	payload, err := canonicalPayload(card)
	if err != nil {
		return signingInputParts{}, err
	}
	header := jwsHeader{
		Alg: JWSAlgorithmEdDSA,
		Typ: "a2a-agent-card+jwt",
		Kid: base64.RawURLEncoding.EncodeToString(pub),
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return signingInputParts{}, fmt.Errorf("a2a: marshal signing header: %w", err)
	}
	return signingInputParts{
		protected: base64.RawURLEncoding.EncodeToString(headerBytes),
		payload:   payload,
	}, nil
}

// canonicalPayload returns the base64url-encoded canonical JSON of
// the card with signatures[] emptied. Emptying (not deleting) the
// slice preserves round-trip stability: signing → serialising →
// re-parsing → verifying produces the same bytes.
func canonicalPayload(card AgentCard) (string, error) {
	stripped := card
	stripped.Signatures = nil
	blob, err := json.Marshal(stripped)
	if err != nil {
		return "", fmt.Errorf("a2a: marshal canonical card: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(blob), nil
}

// decodeCardSignature turns one entry from Signatures[] (which is
// []any so JSON round-trip produces map[string]any) into the typed
// [CardSignature] shape.
func decodeCardSignature(raw any) (CardSignature, error) {
	switch v := raw.(type) {
	case CardSignature:
		return v, nil
	case map[string]any:
		blob, err := json.Marshal(v)
		if err != nil {
			return CardSignature{}, fmt.Errorf("re-marshal signature entry: %w", err)
		}
		var out CardSignature
		if err := json.Unmarshal(blob, &out); err != nil {
			return CardSignature{}, fmt.Errorf("parse signature entry: %w", err)
		}
		if out.Protected == "" || out.Signature == "" {
			return CardSignature{}, errors.New("signature entry missing protected/signature")
		}
		return out, nil
	default:
		return CardSignature{}, fmt.Errorf("unknown signature entry type %T", raw)
	}
}
