package skills_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/skills"
)

// TestVerify_UnreadableSigPathSurfacesStatError distinguishes "no
// signature" from "the path is broken": a skill nested under a regular
// file makes the .sig stat fail with ENOTDIR, which must NOT be
// reported as ErrUnsigned.
func TestVerify_UnreadableSigPathSurfacesStatError(t *testing.T) {
	tmp := t.TempDir()
	_, pub := generateSSHKeyPair(t, tmp)
	allowed := writeAllowedSigners(t, tmp, pub, "test-signer")

	notADir := filepath.Join(tmp, "regular-file")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))

	v := &skills.SSHKeygenVerifier{AllowedSignersFile: allowed, Signer: "test-signer"}
	err := v.Verify(context.Background(), filepath.Join(notADir, "nested.md"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, skills.ErrUnsigned)
	assert.Contains(t, err.Error(), "stat sig")
}

// TestVerify_SignedBodyMissingIsAnError covers a signature left behind
// after its skill file was deleted.
func TestVerify_SignedBodyMissingIsAnError(t *testing.T) {
	tmp := t.TempDir()
	priv, pub := generateSSHKeyPair(t, tmp)
	allowed := writeAllowedSigners(t, tmp, pub, "test-signer")

	skillDir := filepath.Join(tmp, "skills")
	path := writeSkill(t, skillDir, "orphan", "body")
	signBlob(t, priv, "rousseau-skills", path)
	require.NoError(t, os.Remove(path))

	v := &skills.SSHKeygenVerifier{AllowedSignersFile: allowed, Signer: "test-signer"}
	err := v.Verify(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read ")
}

// TestVerify_DerivesSignerFromAllowedSignersFile exercises the
// unpinned-signer path: the identity is taken from the first real entry,
// skipping comments and blank lines.
func TestVerify_DerivesSignerFromAllowedSignersFile(t *testing.T) {
	tmp := t.TempDir()
	priv, pub := generateSSHKeyPair(t, tmp)

	pubBytes, err := os.ReadFile(pub)
	require.NoError(t, err)
	allowed := filepath.Join(tmp, "allowed_signers")
	require.NoError(t, os.WriteFile(allowed,
		[]byte("# leading comment\n\n   \nderived-signer "+string(pubBytes)), 0o600))

	skillDir := filepath.Join(tmp, "skills")
	path := writeSkill(t, skillDir, "greet", "hello world")
	signBlob(t, priv, "rousseau-skills", path)

	v := &skills.SSHKeygenVerifier{AllowedSignersFile: allowed}
	assert.NoError(t, v.Verify(context.Background(), path))
}

// TestVerify_UnderivableSignerIsAnError covers both failure modes of the
// signer-derivation step.
func TestVerify_UnderivableSignerIsAnError(t *testing.T) {
	tmp := t.TempDir()
	priv, _ := generateSSHKeyPair(t, tmp)
	skillDir := filepath.Join(tmp, "skills")
	path := writeSkill(t, skillDir, "greet", "hello world")
	signBlob(t, priv, "rousseau-skills", path)

	noIdentities := filepath.Join(tmp, "empty_signers")
	require.NoError(t, os.WriteFile(noIdentities,
		[]byte("# only a comment\n\nsolo-token-no-key\n"), 0o600))

	unreadable := filepath.Join(tmp, "signers_dir")
	require.NoError(t, os.Mkdir(unreadable, 0o755))

	tests := []struct {
		name    string
		signers string
	}{
		{"file has no identities", noIdentities},
		{"file is not readable as a file", unreadable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := &skills.SSHKeygenVerifier{AllowedSignersFile: tc.signers}
			err := v.Verify(context.Background(), path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no signer pinned and none derivable")
		})
	}
}

// TestLoadVerified_NilLoggerStillFilters proves the default logger
// fallback does not panic while dropping an unsigned skill.
func TestLoadVerified_NilLoggerStillFilters(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "skills")
	writeSkill(t, skillDir, "unsigned", "no sig here")

	out, err := skills.LoadVerified(context.Background(), skillDir, skills.VerifyOptions{
		Verifier: alwaysFailVerifier{},
		Strict:   true,
	})
	require.NoError(t, err)
	assert.Empty(t, out)
}

type alwaysFailVerifier struct{}

func (alwaysFailVerifier) Verify(context.Context, string) error { return skills.ErrUnsigned }

// TestFromDir_LoadErrorPropagates proves a corrupt skill file fails the
// provider construction rather than being silently skipped.
func TestFromDir_LoadErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.md"),
		[]byte("---\nname: [unterminated\n---\nbody\n"), 0o600))

	p, err := skills.FromDir(dir)
	require.Error(t, err)
	assert.Nil(t, p)
}

// TestLoad_UnreadableMarkdownEntryErrors covers a dangling symlink that
// still looks like a skill file to the directory walk.
func TestLoad_UnreadableMarkdownEntryErrors(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(dir, "missing-target"),
		filepath.Join(dir, "dangling.md")))

	loaded, err := skills.Load(dir)
	require.Error(t, err)
	assert.Nil(t, loaded)
	assert.Contains(t, err.Error(), "dangling.md")
}

// TestSystemAppendix_JoinsMultipleTextBlocks proves every text block of
// the latest user message contributes to trigger matching, not just the
// first.
func TestSystemAppendix_JoinsMultipleTextBlocks(t *testing.T) {
	p := skills.NewProvider([]skills.Skill{{
		Name:     "deploy",
		Triggers: []string{"kubernetes"},
		Body:     "run kubectl apply",
	}})

	sess := agent.NewSession("multi-block")
	sess.Messages = append(sess.Messages, agent.Message{
		Role: agent.RoleUser,
		Content: []agent.Content{
			{Kind: agent.ContentText, Text: "please help"},
			{Kind: agent.ContentText, Text: "with kubernetes"},
		},
	})

	appendix := p.SystemAppendix(sess)
	assert.Contains(t, appendix, "run kubectl apply",
		"the trigger only appears in the second text block")
}
