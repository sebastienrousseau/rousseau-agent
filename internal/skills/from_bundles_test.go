package skills_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/skills"
	"github.com/sebastienrousseau/rousseau-agent/internal/skills/bundle"
)

// writeBundle mints and drops a signed .skill.json into dir,
// returning the trusted public key so the test can verify.
func writeBundle(t *testing.T, dir, name string) ed25519.PublicKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	b := &bundle.Bundle{
		Manifest: bundle.Manifest{
			Name: name, Version: "1.0.0", Publisher: "test",
			PublishedAt: time.Now().UTC().Format(time.RFC3339),
			Description: "desc for " + name,
			Triggers:    []string{name},
		},
		Content: "body of " + name,
	}
	b.PopulateHashes()
	b.Signature = bundle.Sign(b.Manifest, priv)
	data, err := json.Marshal(b)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".skill.json"), data, 0o600))
	return pub
}

func TestLoadBundles_MissingDirReturnsNil(t *testing.T) {
	got, err := skills.LoadBundles("/does/not/exist", skills.BundleLoadOptions{})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLoadBundles_EmptyDirReturnsNil(t *testing.T) {
	got, err := skills.LoadBundles(t.TempDir(), skills.BundleLoadOptions{})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLoadBundles_HappyPath(t *testing.T) {
	dir := t.TempDir()
	pub := writeBundle(t, dir, "git-rebase")
	got, err := skills.LoadBundles(dir, skills.BundleLoadOptions{
		TrustedPublisherKeys: []ed25519.PublicKey{pub},
		Logger:               silentLogger(),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "git-rebase", got[0].Name)
	assert.Equal(t, "desc for git-rebase", got[0].Description)
	assert.Equal(t, []string{"git-rebase"}, got[0].Triggers)
	assert.Equal(t, "body of git-rebase", got[0].Body)
}

func TestLoadBundles_UntrustedPublisherDropped(t *testing.T) {
	// Load-bearing property: a valid bundle from a publisher
	// key NOT in the trust list must be dropped. No skill
	// slips through with an unrecognised signer.
	dir := t.TempDir()
	_ = writeBundle(t, dir, "spooky") // real signer's key not passed in
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	got, err := skills.LoadBundles(dir, skills.BundleLoadOptions{
		TrustedPublisherKeys: []ed25519.PublicKey{otherPub},
		Logger:               silentLogger(),
	})
	require.NoError(t, err)
	assert.Empty(t, got, "bundle signed by untrusted key must be dropped")
}

func TestLoadBundles_MalformedJSONDropped(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.skill.json"), []byte("not-json"), 0o600))
	got, err := skills.LoadBundles(dir, skills.BundleLoadOptions{
		Logger: silentLogger(),
	})
	require.NoError(t, err, "one bad file must NOT abort the whole load")
	assert.Empty(t, got)
}

func TestLoadBundles_IgnoresNonBundleFiles(t *testing.T) {
	// Property: only *.skill.json files are considered. A
	// stray .md file in the same dir doesn't crash the loader.
	dir := t.TempDir()
	pub := writeBundle(t, dir, "kept")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "not-a-bundle.md"), []byte("# md"), 0o600))
	got, err := skills.LoadBundles(dir, skills.BundleLoadOptions{
		TrustedPublisherKeys: []ed25519.PublicKey{pub},
		Logger:               silentLogger(),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "kept", got[0].Name)
}

func TestLoadBundles_MixedTrustLoadsOnlyVerified(t *testing.T) {
	// Two bundles, only one signed by a trusted key. Loader
	// returns just the trusted one; the untrusted one is
	// silently dropped.
	dir := t.TempDir()
	pub := writeBundle(t, dir, "trusted")
	_ = writeBundle(t, dir, "untrusted") // different key
	got, err := skills.LoadBundles(dir, skills.BundleLoadOptions{
		TrustedPublisherKeys: []ed25519.PublicKey{pub},
		Logger:               silentLogger(),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "trusted", got[0].Name)
}

func TestLoadBundles_NilLoggerFallsBackToDefault(t *testing.T) {
	// Defensive: nil logger must not panic. Uses
	// slog.Default() internally.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.skill.json"), []byte("bad"), 0o600))
	require.NotPanics(t, func() {
		_, _ = skills.LoadBundles(dir, skills.BundleLoadOptions{}) //nolint:errcheck // panic-guard test
	})
}
