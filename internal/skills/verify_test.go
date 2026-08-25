package skills_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/skills"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// generateSSHKeyPair returns paths to a fresh ed25519 keypair, or
// skips the test if ssh-keygen isn't available.
func generateSSHKeyPair(t *testing.T, dir string) (privKey, pubKey string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("ssh-keygen fixture unsupported on Windows CI")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skipf("ssh-keygen not on PATH: %v", err)
	}
	privKey = filepath.Join(dir, "id")
	pubKey = privKey + ".pub"
	// ed25519, no passphrase, no comment prompt.
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", privKey, "-N", "", "-q") //nolint:gosec // test fixture
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "keygen: %s", string(out))
	return privKey, pubKey
}

// signBlob runs `ssh-keygen -Y sign` and returns the path to the
// signature file (path + ".sig").
func signBlob(t *testing.T, privKey, namespace, path string) string {
	t.Helper()
	cmd := exec.Command("ssh-keygen", "-Y", "sign", "-f", privKey, "-n", namespace, path) //nolint:gosec // test fixture
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "sign: %s", string(out))
	return path + ".sig"
}

// writeAllowedSigners builds an allowed_signers file naming the
// public key under identity "test-signer".
func writeAllowedSigners(t *testing.T, dir, pubKey, identity string) string {
	t.Helper()
	pub, err := os.ReadFile(pubKey)
	require.NoError(t, err)
	out := filepath.Join(dir, "allowed_signers")
	require.NoError(t, os.WriteFile(out, []byte(identity+" "+string(pub)), 0o600))
	return out
}

// writeSkill drops a minimal skill file under dir and returns its
// path. Body contents don't matter for signature tests.
func writeSkill(t *testing.T, dir, name, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name+".md")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func TestSSHKeygenVerifier_ValidSignaturePasses(t *testing.T) {
	tmp := t.TempDir()
	priv, pub := generateSSHKeyPair(t, tmp)
	allowed := writeAllowedSigners(t, tmp, pub, "test-signer")

	skillDir := filepath.Join(tmp, "skills")
	path := writeSkill(t, skillDir, "greet", "hello world")
	signBlob(t, priv, "rousseau-skills", path)

	v := &skills.SSHKeygenVerifier{
		AllowedSignersFile: allowed,
		Signer:             "test-signer",
	}
	assert.NoError(t, v.Verify(context.Background(), path))
}

func TestSSHKeygenVerifier_MissingSigReturnsUnsigned(t *testing.T) {
	tmp := t.TempDir()
	_, pub := generateSSHKeyPair(t, tmp)
	allowed := writeAllowedSigners(t, tmp, pub, "test-signer")

	skillDir := filepath.Join(tmp, "skills")
	path := writeSkill(t, skillDir, "unsigned", "no sig here")

	v := &skills.SSHKeygenVerifier{AllowedSignersFile: allowed, Signer: "test-signer"}
	err := v.Verify(context.Background(), path)
	assert.ErrorIs(t, err, skills.ErrUnsigned)
}

func TestSSHKeygenVerifier_TamperedFileReturnsBadSignature(t *testing.T) {
	tmp := t.TempDir()
	priv, pub := generateSSHKeyPair(t, tmp)
	allowed := writeAllowedSigners(t, tmp, pub, "test-signer")

	skillDir := filepath.Join(tmp, "skills")
	path := writeSkill(t, skillDir, "greet", "original body")
	signBlob(t, priv, "rousseau-skills", path)

	// Tamper with the file AFTER signing.
	require.NoError(t, os.WriteFile(path, []byte("modified body"), 0o644))

	v := &skills.SSHKeygenVerifier{AllowedSignersFile: allowed, Signer: "test-signer"}
	err := v.Verify(context.Background(), path)
	assert.ErrorIs(t, err, skills.ErrBadSignature)
}

func TestSSHKeygenVerifier_WrongNamespaceReturnsBadSignature(t *testing.T) {
	tmp := t.TempDir()
	priv, pub := generateSSHKeyPair(t, tmp)
	allowed := writeAllowedSigners(t, tmp, pub, "test-signer")

	skillDir := filepath.Join(tmp, "skills")
	path := writeSkill(t, skillDir, "greet", "hi")
	signBlob(t, priv, "some-other-namespace", path)

	v := &skills.SSHKeygenVerifier{
		AllowedSignersFile: allowed,
		Signer:             "test-signer",
		Namespace:          "rousseau-skills", // mismatched
	}
	err := v.Verify(context.Background(), path)
	assert.ErrorIs(t, err, skills.ErrBadSignature)
}

func TestSSHKeygenVerifier_MissingAllowedSignersFileFails(t *testing.T) {
	tmp := t.TempDir()
	v := &skills.SSHKeygenVerifier{AllowedSignersFile: filepath.Join(tmp, "does-not-exist")}
	err := v.Verify(context.Background(), filepath.Join(tmp, "irrelevant.md"))
	assert.Error(t, err)
	assert.NotErrorIs(t, err, skills.ErrUnsigned)
}

func TestSSHKeygenVerifier_EmptyAllowedSignersFilePathIsError(t *testing.T) {
	tmp := t.TempDir()
	// Skill file with a sig so we don't short-circuit on Unsigned.
	priv, pub := generateSSHKeyPair(t, tmp)
	_ = pub
	skillDir := filepath.Join(tmp, "skills")
	path := writeSkill(t, skillDir, "greet", "hi")
	signBlob(t, priv, "rousseau-skills", path)

	v := &skills.SSHKeygenVerifier{AllowedSignersFile: ""}
	err := v.Verify(context.Background(), path)
	assert.Error(t, err)
}

func TestLoadVerified_StrictDropsUnsigned(t *testing.T) {
	tmp := t.TempDir()
	priv, pub := generateSSHKeyPair(t, tmp)
	allowed := writeAllowedSigners(t, tmp, pub, "test-signer")

	skillDir := filepath.Join(tmp, "skills")
	writeSkill(t, skillDir, "greet-signed", "hi")
	signBlob(t, priv, "rousseau-skills", filepath.Join(skillDir, "greet-signed.md"))
	writeSkill(t, skillDir, "greet-unsigned", "hi")

	loaded, err := skills.LoadVerified(context.Background(), skillDir, skills.VerifyOptions{
		Verifier: &skills.SSHKeygenVerifier{AllowedSignersFile: allowed, Signer: "test-signer"},
		Strict:   true,
		Logger:   silentLogger(),
	})
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, "greet-signed", loaded[0].Name)
}

func TestLoadVerified_NonStrictKeepsUnsigned(t *testing.T) {
	tmp := t.TempDir()
	_, pub := generateSSHKeyPair(t, tmp)
	allowed := writeAllowedSigners(t, tmp, pub, "test-signer")

	skillDir := filepath.Join(tmp, "skills")
	writeSkill(t, skillDir, "unsigned", "hi")

	loaded, err := skills.LoadVerified(context.Background(), skillDir, skills.VerifyOptions{
		Verifier: &skills.SSHKeygenVerifier{AllowedSignersFile: allowed, Signer: "test-signer"},
		Strict:   false,
		Logger:   silentLogger(),
	})
	require.NoError(t, err)
	assert.Len(t, loaded, 1, "non-strict must keep unsigned skills, only WARN")
}

func TestLoadVerified_NilVerifierIsPassthrough(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "skills")
	writeSkill(t, skillDir, "a", "hi")
	writeSkill(t, skillDir, "b", "hi")

	loaded, err := skills.LoadVerified(context.Background(), skillDir, skills.VerifyOptions{
		Verifier: nil,
	})
	require.NoError(t, err)
	assert.Len(t, loaded, 2)
}

func TestLoadVerified_UnderlyingLoadErrorPropagates(t *testing.T) {
	// Passing a path that IS a file, not a directory, forces Load to
	// fail. LoadVerified must surface the error.
	tmp := t.TempDir()
	f := filepath.Join(tmp, "not-a-dir")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))

	_, err := skills.LoadVerified(context.Background(), f, skills.VerifyOptions{})
	assert.Error(t, err)
	// Confirm it's the wrapped io error, not one of our sentinels.
	assert.False(t, errors.Is(err, skills.ErrUnsigned))
	assert.False(t, errors.Is(err, skills.ErrBadSignature))
}
