package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/skills/bundle"
)

// writeKeyFile drops a base64-encoded raw Ed25519 private key
// into a temp file so tests exercise the loader end-to-end.
func writeKeyFile(t *testing.T) (path string, pub ed25519.PublicKey) {
	t.Helper()
	pubK, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	dir := t.TempDir()
	path = filepath.Join(dir, "sk.b64")
	require.NoError(t, os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600))
	return path, pubK
}

func writeManifest(t *testing.T, dir string, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	p := filepath.Join(dir, "manifest.json")
	require.NoError(t, os.WriteFile(p, b, 0o600))
	return p
}

// -- loadEd25519Private --

func TestLoadEd25519Private_Valid(t *testing.T) {
	path, _ := writeKeyFile(t)
	got, err := loadEd25519Private(path)
	require.NoError(t, err)
	assert.Len(t, got, ed25519.PrivateKeySize)
}

func TestLoadEd25519Private_TrimsWhitespace(t *testing.T) {
	// Publishers editing key files in text editors add
	// trailing newlines / spaces. The loader must tolerate
	// them.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, "sk.b64")
	body := "  " + base64.StdEncoding.EncodeToString(priv) + "\n\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	got, err := loadEd25519Private(path)
	require.NoError(t, err)
	assert.Equal(t, priv, got)
}

func TestLoadEd25519Private_WrongSizeRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sk.b64")
	// 32 bytes decoded (Ed25519 public key size) — not the
	// expected 64-byte private key.
	short := base64.StdEncoding.EncodeToString(make([]byte, 32))
	require.NoError(t, os.WriteFile(path, []byte(short), 0o600))
	_, err := loadEd25519Private(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Ed25519 raw private key")
}

func TestLoadEd25519Private_NotBase64Rejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sk.b64")
	require.NoError(t, os.WriteFile(path, []byte("!!! not base64 !!!"), 0o600))
	_, err := loadEd25519Private(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestLoadEd25519Private_MissingFileErrors(t *testing.T) {
	_, err := loadEd25519Private("/does/not/exist")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read key")
}

// -- assembleBundle end-to-end --

func TestAssembleBundle_HappyPathVerifies(t *testing.T) {
	keyPath, pub := writeKeyFile(t)
	dir := t.TempDir()
	manifestPath := writeManifest(t, dir, map[string]any{
		"name":         "git-rebase",
		"version":      "1.2.0",
		"publisher":    "vendor-example",
		"published_at": "2026-08-15T00:00:00Z",
		"triggers":     []string{"rebase"},
	})
	contentPath := filepath.Join(dir, "content.md")
	require.NoError(t, os.WriteFile(contentPath, []byte("# git-rebase\nBody."), 0o600))

	b, err := assembleBundle(manifestPath, contentPath, "", keyPath)
	require.NoError(t, err)
	// Self-verify already happened inside assembleBundle, but
	// re-checking here makes the property explicit.
	require.NoError(t, b.Verify([]ed25519.PublicKey{pub}))
	assert.Equal(t, "git-rebase", b.Manifest.Name)
	assert.NotEmpty(t, b.Manifest.ContentSHA256)
}

func TestAssembleBundle_WithSBOM(t *testing.T) {
	keyPath, pub := writeKeyFile(t)
	dir := t.TempDir()
	manifestPath := writeManifest(t, dir, map[string]any{
		"name": "with-sbom", "version": "1.0.0", "publisher": "p",
		"published_at": "2026-01-01T00:00:00Z",
	})
	contentPath := filepath.Join(dir, "c.md")
	require.NoError(t, os.WriteFile(contentPath, []byte("body"), 0o600))
	sbomPath := filepath.Join(dir, "sbom.json")
	require.NoError(t, os.WriteFile(sbomPath, []byte(`{"bomFormat":"CycloneDX","components":[]}`), 0o600))

	b, err := assembleBundle(manifestPath, contentPath, sbomPath, keyPath)
	require.NoError(t, err)
	require.NoError(t, b.Verify([]ed25519.PublicKey{pub}))
	assert.NotEmpty(t, b.Manifest.SBOMSHA256)
	assert.NotEmpty(t, b.SBOM)
}

func TestAssembleBundle_MalformedSBOMRejected(t *testing.T) {
	// Publisher-tooling guard: don't ship a bundle with an
	// unparseable SBOM. Operators reading the bundle expect
	// valid JSON.
	keyPath, _ := writeKeyFile(t)
	dir := t.TempDir()
	manifestPath := writeManifest(t, dir, map[string]any{
		"name": "x", "version": "1.0.0", "publisher": "p",
		"published_at": "2026-01-01T00:00:00Z",
	})
	contentPath := filepath.Join(dir, "c.md")
	require.NoError(t, os.WriteFile(contentPath, []byte("body"), 0o600))
	sbomPath := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(sbomPath, []byte("not-json"), 0o600))

	_, err := assembleBundle(manifestPath, contentPath, sbomPath, keyPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

func TestAssembleBundle_MissingManifestErrors(t *testing.T) {
	keyPath, _ := writeKeyFile(t)
	dir := t.TempDir()
	contentPath := filepath.Join(dir, "c.md")
	require.NoError(t, os.WriteFile(contentPath, []byte("body"), 0o600))
	_, err := assembleBundle("/does/not/exist", contentPath, "", keyPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read manifest")
}

func TestAssembleBundle_MalformedManifestJSONErrors(t *testing.T) {
	keyPath, _ := writeKeyFile(t)
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "m.json")
	require.NoError(t, os.WriteFile(manifestPath, []byte("{ not valid"), 0o600))
	contentPath := filepath.Join(dir, "c.md")
	require.NoError(t, os.WriteFile(contentPath, []byte("body"), 0o600))
	_, err := assembleBundle(manifestPath, contentPath, "", keyPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse manifest")
}

func TestAssembleBundle_MissingContentErrors(t *testing.T) {
	keyPath, _ := writeKeyFile(t)
	dir := t.TempDir()
	manifestPath := writeManifest(t, dir, map[string]any{
		"name": "x", "version": "1.0.0", "publisher": "p",
		"published_at": "2026-01-01T00:00:00Z",
	})
	_, err := assembleBundle(manifestPath, "/does/not/exist", "", keyPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read content")
}

func TestAssembleBundle_ManifestValidationFires(t *testing.T) {
	// Empty name — bundle.Validate (called via Verify at the
	// end of assembleBundle) must catch it.
	keyPath, _ := writeKeyFile(t)
	dir := t.TempDir()
	manifestPath := writeManifest(t, dir, map[string]any{
		"version": "1.0.0", "publisher": "p",
		"published_at": "2026-01-01T00:00:00Z",
		// name deliberately absent
	})
	contentPath := filepath.Join(dir, "c.md")
	require.NoError(t, os.WriteFile(contentPath, []byte("body"), 0o600))
	_, err := assembleBundle(manifestPath, contentPath, "", keyPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, bundle.ErrMalformedManifest)
}

// -- newSkillsSignCmd --

func TestSkillsSignCmd_MissingFlagsRejected(t *testing.T) {
	// Every required flag has a hand-rolled check because
	// cobra's MarkFlagRequired can't express the tri-
	// condition. Confirm the message is legible.
	cmd := newSkillsSignCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestSkillsSignCmd_WritesSignedBundleToOut(t *testing.T) {
	keyPath, pub := writeKeyFile(t)
	dir := t.TempDir()
	manifestPath := writeManifest(t, dir, map[string]any{
		"name": "cli-e2e", "version": "0.1.0", "publisher": "p",
		"published_at": "2026-01-01T00:00:00Z",
	})
	contentPath := filepath.Join(dir, "c.md")
	require.NoError(t, os.WriteFile(contentPath, []byte("body"), 0o600))
	outPath := filepath.Join(dir, "signed.skill.json")

	cmd := newSkillsSignCmd()
	cmd.SetArgs([]string{
		"--manifest", manifestPath,
		"--content-file", contentPath,
		"--key", keyPath,
		"--out", outPath,
	})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	require.NoError(t, cmd.Execute())

	// Re-read + verify with the public key.
	loaded, err := bundle.Load(outPath)
	require.NoError(t, err)
	require.NoError(t, loaded.Verify([]ed25519.PublicKey{pub}))
	assert.Equal(t, "cli-e2e", loaded.Manifest.Name)
}

func TestSkillsSignCmd_StdoutWhenOutIsDash(t *testing.T) {
	keyPath, pub := writeKeyFile(t)
	dir := t.TempDir()
	manifestPath := writeManifest(t, dir, map[string]any{
		"name": "stdout-e2e", "version": "0.1.0", "publisher": "p",
		"published_at": "2026-01-01T00:00:00Z",
	})
	contentPath := filepath.Join(dir, "c.md")
	require.NoError(t, os.WriteFile(contentPath, []byte("body"), 0o600))

	cmd := newSkillsSignCmd()
	cmd.SetArgs([]string{
		"--manifest", manifestPath,
		"--content-file", contentPath,
		"--key", keyPath,
		"--out", "-",
	})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	loaded, err := bundle.Parse(buf.Bytes())
	require.NoError(t, err)
	require.NoError(t, loaded.Verify([]ed25519.PublicKey{pub}))
}
