package fsguard

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolatedHome points HOME and the XDG dirs at a temp directory so the
// default lists are computed for a throwaway user.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	return home
}

// Credentials and history the original list missed are denied for
// reading as well as writing.
func TestGuard_DeniesNewSecretPaths(t *testing.T) {
	home := isolatedHome(t)
	g, err := New("", nil)
	require.NoError(t, err)
	for _, rel := range []string{
		".config/gh/hosts.yml", ".config/gcloud/credentials.db", ".azure/accessTokens.json",
		".npmrc", ".pypirc", ".cargo/credentials.toml", ".password-store/bank.gpg",
		".bash_history", ".zsh_history", ".local/share/fish/fish_history",
		".local/state/rousseau/audit-chain.key",
	} {
		_, err := g.Resolve(filepath.Join(home, rel))
		assert.ErrorIs(t, err, ErrDenied, rel)
	}
}

// Shell start-up files, autostart entries and git hooks may be read
// but never written: a write there runs code outside any sandbox.
func TestGuard_WriteDenyKeepsReadsWorking(t *testing.T) {
	home := isolatedHome(t)
	g, err := New("", nil)
	require.NoError(t, err)
	for _, rel := range []string{
		".bashrc", ".zshrc", ".profile", ".config/fish/config.fish",
		".config/systemd/user/evil.service", ".config/autostart/evil.desktop", ".local/bin/ls",
	} {
		p := filepath.Join(home, rel)
		_, err := g.Resolve(p)
		assert.NoError(t, err, "read of %s stays allowed", rel)
		_, err = g.ResolveForWrite(p)
		assert.ErrorIs(t, err, ErrWriteDenied, rel)
	}
}

func TestGuard_GitHooksWriteDenied(t *testing.T) {
	isolatedHome(t)
	ws := t.TempDir()
	g, err := New(ws, nil)
	require.NoError(t, err)
	_, err = g.ResolveForWrite(filepath.Join(ws, "repo", ".git", "hooks", "pre-commit"))
	assert.ErrorIs(t, err, ErrWriteDenied)
	_, err = g.ResolveForWrite(filepath.Join(ws, "repo", ".git", "config"))
	assert.NoError(t, err, "other .git files are not covered by this rule")
	_, err = g.ResolveForWrite(filepath.Join(ws, "repo", "hooks", "x"))
	assert.NoError(t, err)
	_, err = g.ResolveForWrite(filepath.Join(ws, "repo", "x.go"))
	assert.NoError(t, err)
}

// On case-insensitive file systems ~/.SSH is ~/.ssh.
func TestGuard_CaseFoldOnCaseInsensitiveFS(t *testing.T) {
	home := isolatedHome(t)
	prev := caseInsensitiveFS
	caseInsensitiveFS = true
	t.Cleanup(func() { caseInsensitiveFS = prev })
	g, err := New("", nil)
	require.NoError(t, err)
	_, err = g.Resolve(filepath.Join(home, ".SSH", "id_ed25519"))
	assert.ErrorIs(t, err, ErrDenied)
	_, err = g.ResolveForWrite(filepath.Join(home, ".BashRC"))
	assert.ErrorIs(t, err, ErrWriteDenied)
	_, err = g.ResolveForWrite(filepath.Join(home, "w", ".GIT", "Hooks", "pre-push"))
	assert.ErrorIs(t, err, ErrWriteDenied)
}

func TestDefaultWriteDeny_NoHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("platform resolves a home directory without $HOME")
	}
	assert.Empty(t, DefaultWriteDeny())
	assert.False(t, errors.Is(nil, ErrWriteDenied))
}
