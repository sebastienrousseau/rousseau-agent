// Package bundle implements the enterprise-only signed-skills
// bundle format — ROADMAP §2.8, gated on
// [license.FeatureGovernanceAdvanced].
//
// A **bundle** is a single JSON file (extension `.skill.json`)
// carrying a skill's content, a publisher-signed provenance
// attestation, and an optional CycloneDX SBOM. Compliance
// teams (or regulated buyers) get three things the free
// SSH-signed-markdown loader can't cheaply provide:
//
//  1. Cryptographic provenance — every bundle is Ed25519-signed
//     over a canonical hash of its contents.
//  2. SBOM per skill — the operator can inventory exactly what
//     model-invoked tooling is on their fleet.
//  3. Centralised catalogue — the loader reads bundles from a
//     dedicated directory; unsigned files are refused.
//
// # Wire format
//
//	{
//	  "manifest": {
//	    "name":         "git-rebase",
//	    "version":      "1.2.0",
//	    "publisher":    "vendor-example",
//	    "published_at": "2026-08-15T00:00:00Z",
//	    "description":  "…",
//	    "triggers":     ["rebase", "git rebase"],
//	    "content_sha256": "<hex>",
//	    "sbom_sha256":    "<hex>"       // "" when SBOM absent
//	  },
//	  "content": "…skill markdown…",
//	  "sbom":    { … CycloneDX … } | null,
//	  "signature": {
//	    "algorithm":  "ed25519",
//	    "public_key": "<base64>",
//	    "signature":  "<base64>",
//	    "signed_at":  "2026-08-15T00:00:01Z"
//	  }
//	}
//
// The signature covers the canonical hash defined by
// [ManifestHash] — a domain-separated SHA-256 over the
// manifest's field bytes plus the content and SBOM hashes. The
// SBOM itself isn't signed byte-by-byte; instead the manifest
// pins [Manifest.SBOMSHA256], and the loader recomputes it on
// load to catch tampering.
//
// # Trust model
//
// Publishers are Ed25519 keypairs. The daemon carries an
// operator-supplied list of trusted publisher public keys; a
// bundle whose signature.public_key isn't in that list is
// refused. This mirrors the license package's embedded-keyring
// pattern — trust root is operator-configured, not implicit.
package bundle

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Manifest carries the skill's identifying metadata plus the
// hashes over the payload segments. Every field participates in
// the signed hash; adding one is a wire-format break.
type Manifest struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Publisher     string   `json:"publisher"`
	PublishedAt   string   `json:"published_at"` // RFC3339
	Description   string   `json:"description,omitempty"`
	Triggers      []string `json:"triggers,omitempty"`
	ContentSHA256 string   `json:"content_sha256"`
	SBOMSHA256    string   `json:"sbom_sha256,omitempty"`
}

// Signature is the Ed25519 attestation over the manifest.
type Signature struct {
	Algorithm string `json:"algorithm"`  // MUST be "ed25519"
	PublicKey string `json:"public_key"` // base64 std
	Signature string `json:"signature"`  // base64 std
	SignedAt  string `json:"signed_at"`  // RFC3339
}

// Bundle is one parsed .skill.json file.
type Bundle struct {
	Manifest  Manifest        `json:"manifest"`
	Content   string          `json:"content"`
	SBOM      json.RawMessage `json:"sbom,omitempty"`
	Signature Signature       `json:"signature"`
}

// Errors returned from Verify / Load.
var (
	// ErrUnsupportedAlgorithm is returned when signature.algorithm
	// is anything other than "ed25519".
	ErrUnsupportedAlgorithm = errors.New("bundle: unsupported signature algorithm")
	// ErrUntrustedPublisher is returned when signature.public_key
	// isn't in the operator-supplied trust list.
	ErrUntrustedPublisher = errors.New("bundle: signature key is not in the trust list")
	// ErrBadSignature is returned when the Ed25519 signature
	// doesn't verify against the manifest hash.
	ErrBadSignature = errors.New("bundle: signature verification failed")
	// ErrContentHashMismatch is returned when the manifest's
	// content_sha256 disagrees with the actual content bytes.
	// Guards against a publisher who signed a manifest but
	// swapped in different content post-signing.
	ErrContentHashMismatch = errors.New("bundle: content hash mismatch")
	// ErrSBOMHashMismatch is the SBOM-side twin of the above.
	ErrSBOMHashMismatch = errors.New("bundle: SBOM hash mismatch")
	// ErrMalformedManifest is returned when required manifest
	// fields are empty or the format is otherwise unusable.
	ErrMalformedManifest = errors.New("bundle: manifest malformed")
)

// Load reads a bundle from path. Callers pass the result to
// [Verify] with a trust list before treating the content as
// authoritative — Load itself doesn't check any signature.
func Load(path string) (*Bundle, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied path within the bundles dir
	if err != nil {
		return nil, fmt.Errorf("bundle: read %s: %w", path, err)
	}
	return Parse(b)
}

// Parse decodes bytes into a Bundle. Same shape as [Load] but
// works against in-memory data (tests, alternative sources).
func Parse(b []byte) (*Bundle, error) {
	var bundle Bundle
	if err := json.Unmarshal(b, &bundle); err != nil {
		return nil, fmt.Errorf("bundle: parse: %w", err)
	}
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	return &bundle, nil
}

// Validate checks the manifest for required fields. Called by
// [Parse] but also useful for callers building bundles in code.
func (b *Bundle) Validate() error {
	if b.Manifest.Name == "" {
		return fmt.Errorf("%w: name is required", ErrMalformedManifest)
	}
	if b.Manifest.Version == "" {
		return fmt.Errorf("%w: version is required", ErrMalformedManifest)
	}
	if b.Manifest.Publisher == "" {
		return fmt.Errorf("%w: publisher is required", ErrMalformedManifest)
	}
	if b.Manifest.PublishedAt == "" {
		return fmt.Errorf("%w: published_at is required", ErrMalformedManifest)
	}
	if _, err := time.Parse(time.RFC3339, b.Manifest.PublishedAt); err != nil {
		return fmt.Errorf("%w: published_at must be RFC3339: %s", ErrMalformedManifest, err.Error())
	}
	if b.Manifest.ContentSHA256 == "" {
		return fmt.Errorf("%w: content_sha256 is required", ErrMalformedManifest)
	}
	return nil
}

// Verify performs the full trust check:
//
//  1. Signature.Algorithm MUST be "ed25519".
//  2. Signature.PublicKey MUST be present in trustedKeys
//     (operator-supplied allow-list).
//  3. Recomputed content hash MUST match manifest.
//  4. Recomputed SBOM hash MUST match manifest (when SBOM is
//     present).
//  5. Ed25519 verify over the manifest hash MUST pass.
//
// Returns nil on success; a wrapped sentinel error on any
// failure. Callers surface the sentinel to distinguish
// "reject-this-bundle" from "SBOM-was-tampered" for audit
// purposes.
func (b *Bundle) Verify(trustedKeys []ed25519.PublicKey) error {
	if b.Signature.Algorithm != "ed25519" {
		return fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, b.Signature.Algorithm)
	}
	pub, err := base64.StdEncoding.DecodeString(b.Signature.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: cannot decode public_key", ErrBadSignature)
	}
	sig, err := base64.StdEncoding.DecodeString(b.Signature.Signature)
	if err != nil {
		return fmt.Errorf("%w: cannot decode signature", ErrBadSignature)
	}
	// Trust-list check: the key must be one the operator
	// pre-authorised. Constant-time compare isn't security-
	// critical here (the key is public) but we still avoid
	// short-circuiting on the first match for clarity.
	var trusted bool
	for _, t := range trustedKeys {
		if len(t) == len(pub) && bytesEqual(t, pub) {
			trusted = true
			break
		}
	}
	if !trusted {
		return ErrUntrustedPublisher
	}
	// Content hash guard: a publisher signed a specific
	// manifest whose content_sha256 field pinned this exact
	// content. If the shipped content differs, someone swapped
	// the payload post-signing.
	got := sha256.Sum256([]byte(b.Content))
	if hex.EncodeToString(got[:]) != b.Manifest.ContentSHA256 {
		return ErrContentHashMismatch
	}
	// SBOM twin — only when the manifest declares one.
	if b.Manifest.SBOMSHA256 != "" {
		if len(b.SBOM) == 0 {
			return fmt.Errorf("%w: manifest names an SBOM hash but SBOM payload is empty", ErrSBOMHashMismatch)
		}
		sh := sha256.Sum256(b.SBOM)
		if hex.EncodeToString(sh[:]) != b.Manifest.SBOMSHA256 {
			return ErrSBOMHashMismatch
		}
	} else if len(b.SBOM) > 0 {
		return fmt.Errorf("%w: SBOM payload present but manifest declares no hash", ErrSBOMHashMismatch)
	}
	// Signature over the manifest hash.
	msg := ManifestHash(b.Manifest)
	if !ed25519.Verify(pub, msg, sig) {
		return ErrBadSignature
	}
	return nil
}

// ManifestHash produces the deterministic byte sequence the
// publisher signed. Domain-separated with 0x00 to prevent
// cross-field ambiguity. Field order is FROZEN — adding /
// removing / reordering breaks every existing signature.
func ManifestHash(m Manifest) []byte {
	h := sha256.New()
	write := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0x00})
	}
	write(m.Name)
	write(m.Version)
	write(m.Publisher)
	write(m.PublishedAt)
	write(m.Description)
	// Triggers: newline-separated so a rename in the middle
	// hashes differently.
	for _, t := range m.Triggers {
		write(t)
	}
	write(strconv.Itoa(len(m.Triggers))) // guards against boundary ambiguity
	write(m.ContentSHA256)
	write(m.SBOMSHA256)
	return h.Sum(nil)
}

// Sign is the counterpart the publisher-side tooling calls.
// Kept in-package so the sign / verify code lives together and
// drift is impossible. The daemon itself never signs — the
// operator's CI signs at package time.
func Sign(m Manifest, priv ed25519.PrivateKey) Signature {
	sig := ed25519.Sign(priv, ManifestHash(m))
	pub := priv.Public().(ed25519.PublicKey)
	return Signature{
		Algorithm: "ed25519",
		PublicKey: base64.StdEncoding.EncodeToString(pub),
		Signature: base64.StdEncoding.EncodeToString(sig),
		SignedAt:  time.Now().UTC().Format(time.RFC3339),
	}
}

// PopulateHashes fills in the manifest's content_sha256 and
// sbom_sha256 (when applicable) from the bundle's actual
// payloads. Publisher-side helper — call before Sign so the
// signed manifest matches the shipped payload.
func (b *Bundle) PopulateHashes() {
	ch := sha256.Sum256([]byte(b.Content))
	b.Manifest.ContentSHA256 = hex.EncodeToString(ch[:])
	if len(b.SBOM) > 0 {
		sh := sha256.Sum256(b.SBOM)
		b.Manifest.SBOMSHA256 = hex.EncodeToString(sh[:])
	} else {
		b.Manifest.SBOMSHA256 = ""
	}
}

// bytesEqual is a length-checked equality — avoids the
// implicit-length crypto/subtle.ConstantTimeCompare gotcha
// where different-length inputs both return 0.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
