package bundle_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/skills/bundle"
)

// -- helpers -------------------------------------------------------

// keypair mints a fresh Ed25519 keypair for test signing.
func keypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

// buildSigned crafts a well-formed signed bundle for tests.
// The caller can mutate the returned *Bundle before Verify to
// exercise specific tamper paths.
func buildSigned(t *testing.T, priv ed25519.PrivateKey, content string, sbom json.RawMessage) *bundle.Bundle {
	t.Helper()
	b := &bundle.Bundle{
		Manifest: bundle.Manifest{
			Name:        "git-rebase",
			Version:     "1.2.0",
			Publisher:   "vendor-example",
			PublishedAt: time.Now().UTC().Format(time.RFC3339),
			Description: "Guide git rebase safely.",
			Triggers:    []string{"rebase", "git rebase"},
		},
		Content: content,
		SBOM:    sbom,
	}
	b.PopulateHashes()
	b.Signature = bundle.Sign(b.Manifest, priv)
	return b
}

// -- Validate --

func TestValidate_RequiresName(t *testing.T) {
	b := &bundle.Bundle{Manifest: bundle.Manifest{Version: "1", Publisher: "p", PublishedAt: time.Now().Format(time.RFC3339), ContentSHA256: "x"}}
	require.ErrorIs(t, b.Validate(), bundle.ErrMalformedManifest)
}

func TestValidate_RejectsBadTimestamp(t *testing.T) {
	b := &bundle.Bundle{Manifest: bundle.Manifest{
		Name: "n", Version: "1", Publisher: "p",
		PublishedAt: "not-a-timestamp", ContentSHA256: "x",
	}}
	err := b.Validate()
	require.ErrorIs(t, err, bundle.ErrMalformedManifest)
	assert.Contains(t, err.Error(), "RFC3339")
}

// -- Sign / Verify happy paths --

func TestSignAndVerify_Roundtrip(t *testing.T) {
	pub, priv := keypair(t)
	b := buildSigned(t, priv, "skill content", nil)
	require.NoError(t, b.Verify([]ed25519.PublicKey{pub}))
}

func TestVerify_UntrustedPublicKeyRejected(t *testing.T) {
	// A signature that IS valid but from a key the operator
	// hasn't trusted MUST be rejected. Load-bearing: the
	// trust root is the operator's key list, not the mere
	// presence of a signature.
	_, priv := keypair(t)
	otherPub, _ := keypair(t)
	b := buildSigned(t, priv, "skill", nil)
	err := b.Verify([]ed25519.PublicKey{otherPub})
	require.ErrorIs(t, err, bundle.ErrUntrustedPublisher)
}

func TestVerify_TamperedContentRejected(t *testing.T) {
	// Signer signed a manifest whose content_sha256 field
	// pinned the content bytes. Swapping the content post-
	// signing must be caught by the recomputed hash check.
	pub, priv := keypair(t)
	b := buildSigned(t, priv, "original", nil)
	b.Content = "TAMPERED"
	err := b.Verify([]ed25519.PublicKey{pub})
	require.ErrorIs(t, err, bundle.ErrContentHashMismatch)
}

func TestVerify_TamperedManifestRejected(t *testing.T) {
	// Changing a signed manifest field must invalidate the
	// signature. Version bump post-signing is the concrete
	// hostile scenario.
	pub, priv := keypair(t)
	b := buildSigned(t, priv, "skill", nil)
	b.Manifest.Version = "9.9.9"
	err := b.Verify([]ed25519.PublicKey{pub})
	require.ErrorIs(t, err, bundle.ErrBadSignature)
}

func TestVerify_WrongAlgorithmRejected(t *testing.T) {
	pub, priv := keypair(t)
	b := buildSigned(t, priv, "skill", nil)
	b.Signature.Algorithm = "hmac-sha256"
	err := b.Verify([]ed25519.PublicKey{pub})
	require.ErrorIs(t, err, bundle.ErrUnsupportedAlgorithm)
}

func TestVerify_MalformedPublicKeyRejected(t *testing.T) {
	pub, priv := keypair(t)
	b := buildSigned(t, priv, "skill", nil)
	b.Signature.PublicKey = "not-base64!!!"
	err := b.Verify([]ed25519.PublicKey{pub})
	require.ErrorIs(t, err, bundle.ErrBadSignature)
}

func TestVerify_MalformedSignatureRejected(t *testing.T) {
	pub, priv := keypair(t)
	b := buildSigned(t, priv, "skill", nil)
	b.Signature.Signature = "not-base64!!!"
	err := b.Verify([]ed25519.PublicKey{pub})
	require.ErrorIs(t, err, bundle.ErrBadSignature)
}

// -- SBOM invariants --

func TestVerify_SBOMHashMustMatch(t *testing.T) {
	pub, priv := keypair(t)
	sbom := json.RawMessage(`{"bomFormat":"CycloneDX"}`)
	b := buildSigned(t, priv, "skill", sbom)
	// Mutate the SBOM post-sign → hash mismatch.
	b.SBOM = json.RawMessage(`{"bomFormat":"CycloneDX","tampered":true}`)
	err := b.Verify([]ed25519.PublicKey{pub})
	require.ErrorIs(t, err, bundle.ErrSBOMHashMismatch)
}

func TestVerify_MissingSBOMPayloadWhenHashDeclared(t *testing.T) {
	pub, priv := keypair(t)
	sbom := json.RawMessage(`{"bomFormat":"CycloneDX"}`)
	b := buildSigned(t, priv, "skill", sbom)
	// Drop the payload but keep the hash → mismatch.
	b.SBOM = nil
	err := b.Verify([]ed25519.PublicKey{pub})
	require.ErrorIs(t, err, bundle.ErrSBOMHashMismatch)
}

func TestVerify_SBOMPresentWithoutHashRejected(t *testing.T) {
	// Manifest declares no SBOM hash but payload was inserted
	// — the manifest doesn't cover the SBOM, so we can't
	// trust it. Fail-CLOSED.
	pub, priv := keypair(t)
	b := buildSigned(t, priv, "skill", nil)
	// Post-sign, splice in an SBOM. The signature still
	// verifies (manifest hasn't changed) but the mismatched
	// SBOM must be caught.
	b.SBOM = json.RawMessage(`{"attacker":"true"}`)
	err := b.Verify([]ed25519.PublicKey{pub})
	require.ErrorIs(t, err, bundle.ErrSBOMHashMismatch)
}

func TestVerify_SBOMHappyPath(t *testing.T) {
	pub, priv := keypair(t)
	sbom := json.RawMessage(`{"bomFormat":"CycloneDX","components":[]}`)
	b := buildSigned(t, priv, "skill", sbom)
	require.NoError(t, b.Verify([]ed25519.PublicKey{pub}))
}

// -- Parse + Load --

func TestParse_RejectsInvalidJSON(t *testing.T) {
	_, err := bundle.Parse([]byte("not-json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestParse_RejectsMissingRequiredFields(t *testing.T) {
	// Parse runs Validate, so a well-formed JSON without a
	// name still bounces.
	body := `{"manifest":{"version":"1","publisher":"p","published_at":"2026-01-01T00:00:00Z","content_sha256":"x"},"content":"","signature":{"algorithm":"ed25519","public_key":"","signature":"","signed_at":""}}`
	_, err := bundle.Parse([]byte(body))
	require.ErrorIs(t, err, bundle.ErrMalformedManifest)
}

func TestLoad_ReadsFromDisk(t *testing.T) {
	pub, priv := keypair(t)
	b := buildSigned(t, priv, "on-disk skill body", nil)
	data, err := json.Marshal(b)
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "git-rebase.skill.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	loaded, err := bundle.Load(path)
	require.NoError(t, err)
	require.NoError(t, loaded.Verify([]ed25519.PublicKey{pub}))
	assert.Equal(t, "git-rebase", loaded.Manifest.Name)
	assert.Equal(t, "on-disk skill body", loaded.Content)
}

func TestLoad_MissingFileErrors(t *testing.T) {
	_, err := bundle.Load(filepath.Join(t.TempDir(), "no-such.skill.json"))
	require.Error(t, err)
}

// -- ManifestHash determinism --

func TestManifestHash_FieldOrderMattersToSignature(t *testing.T) {
	// Property: swapping two triggers changes the hash. Guards
	// against a signature that "looks right" against a
	// reordered manifest.
	m1 := bundle.Manifest{
		Name: "n", Version: "1", Publisher: "p",
		PublishedAt:   "2026-01-01T00:00:00Z",
		Triggers:      []string{"a", "b"},
		ContentSHA256: "x",
	}
	m2 := m1
	m2.Triggers = []string{"b", "a"}
	assert.NotEqual(t, bundle.ManifestHash(m1), bundle.ManifestHash(m2))
}

func TestManifestHash_TriggerCountBoundary(t *testing.T) {
	// Concat-only hashing would let two triggers "ab" hash
	// the same as one trigger "ab" — we defend by appending
	// the trigger count. Confirm.
	m1 := bundle.Manifest{
		Name: "n", Version: "1", Publisher: "p",
		PublishedAt:   "2026-01-01T00:00:00Z",
		Triggers:      []string{"a", "b"},
		ContentSHA256: "x",
	}
	m2 := m1
	m2.Triggers = []string{"ab"}
	assert.NotEqual(t, bundle.ManifestHash(m1), bundle.ManifestHash(m2))
}

// -- PopulateHashes --

func TestPopulateHashes_ClearsSBOMWhenAbsent(t *testing.T) {
	// Property: a re-published bundle that used to have an
	// SBOM but no longer does must clear the manifest field
	// (otherwise the signed manifest declares a hash for
	// content that isn't there and Verify hard-fails).
	b := &bundle.Bundle{
		Manifest: bundle.Manifest{SBOMSHA256: "old-hash"},
		Content:  "skill",
	}
	b.PopulateHashes()
	assert.Empty(t, b.Manifest.SBOMSHA256)
}

// -- unused import shim so `base64` is exercised in this file --

var _ = base64.StdEncoding.EncodeToString
