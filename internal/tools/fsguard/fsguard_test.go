package fsguard

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve_RejectsRelative(t *testing.T) {
	g, err := New("", nil)
	require.NoError(t, err)
	_, err = g.Resolve("etc/passwd")
	assert.ErrorIs(t, err, ErrRelative)
	_, err = g.Resolve("")
	assert.ErrorIs(t, err, ErrRelative)
}

func TestResolve_DeniesConfigAndCredentialDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	g, err := New("", nil)
	require.NoError(t, err)

	for _, p := range []string{
		filepath.Join(home, ".config", "rousseau", "config.yaml"),
		filepath.Join(home, ".local", "share", "rousseau", "sessions.db"),
		filepath.Join(home, ".ssh", "authorized_keys"),
		filepath.Join(home, ".claude", ".credentials.json"),
		filepath.Join(home, ".aws", "credentials"),
		"/proc/self/environ",
		"/dev/zero",
		"/etc/shadow",
	} {
		_, err := g.Resolve(p)
		assert.ErrorIs(t, err, ErrDenied, p)
	}

	// A sibling directory that merely shares a prefix is allowed.
	ok := filepath.Join(home, ".sshfs", "x")
	require.NoError(t, os.MkdirAll(filepath.Dir(ok), 0o755))
	_, err = g.Resolve(ok)
	assert.NoError(t, err)
}

func TestResolve_WorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	g, err := New(root, nil)
	require.NoError(t, err)

	got, err := g.Resolve(filepath.Join(root, "src", "main.go"))
	require.NoError(t, err)
	realRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(realRoot, "src", "main.go"), got)

	_, err = g.Resolve(filepath.Join(outside, "x"))
	assert.ErrorIs(t, err, ErrOutsideRoot)

	// Dot-dot traversal is cleaned before comparison.
	_, err = g.Resolve(filepath.Join(root, "..", filepath.Base(outside), "x"))
	assert.ErrorIs(t, err, ErrOutsideRoot)
}

func TestResolve_SymlinkEscapeIsCaught(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))

	g, err := New(root, nil)
	require.NoError(t, err)

	// Existing target through the link.
	_, err = g.Resolve(filepath.Join(root, "link", "secret"))
	assert.ErrorIs(t, err, ErrOutsideRoot)
	// Not-yet-existing target through the link (write path).
	_, err = g.Resolve(filepath.Join(root, "link", "new.txt"))
	assert.ErrorIs(t, err, ErrOutsideRoot)
}

func TestResolve_ExtraDenyAndRootInteract(t *testing.T) {
	root := t.TempDir()
	secrets := filepath.Join(root, "secrets")
	require.NoError(t, os.MkdirAll(secrets, 0o755))
	g, err := New(root, []string{secrets})
	require.NoError(t, err)

	_, err = g.Resolve(filepath.Join(secrets, "k"))
	assert.ErrorIs(t, err, ErrDenied)
	_, err = g.Resolve(filepath.Join(root, "ok"))
	assert.NoError(t, err)
}

func TestNew_RejectsRelativeEntries(t *testing.T) {
	_, err := New("relative", nil)
	assert.ErrorIs(t, err, ErrRelative)
	_, err = New("", []string{"relative"})
	assert.ErrorIs(t, err, ErrRelative)
	_, err = New(filepath.Join(t.TempDir(), "missing"), nil)
	assert.NoError(t, err, "a root that does not exist yet resolves through its parent")
}

func TestDefaultDeny_WithoutHomeKeepsSystemEntries(t *testing.T) {
	t.Setenv("HOME", "")
	got := DefaultDeny()
	assert.Contains(t, got, "/proc")
	assert.Len(t, got, 6, "no home-relative entries when $HOME is unset")
}

func TestWithin(t *testing.T) {
	assert.True(t, within("/a/b", "/a"))
	assert.True(t, within("/a", "/a"))
	assert.False(t, within("/ab", "/a"))
	assert.True(t, within("/anything", "/"), "the filesystem root contains every path")
	assert.True(t, within("/", "/"))
}

func TestResolveExisting_FallsBackToCleanedPath(t *testing.T) {
	// A path with no existing ancestor other than "/" still resolves
	// through "/" and keeps its tail intact.
	got := resolveExisting(filepath.Join(string(filepath.Separator), "no-such-dir-xyz", "f"))
	assert.Equal(t, filepath.Join(string(filepath.Separator), "no-such-dir-xyz", "f"), got)
}

func TestDefault_IsSingletonWithDenyList(t *testing.T) {
	assert.Same(t, Default(), Default())
	assert.Empty(t, Default().Root())
	_, err := Default().Resolve("/proc/self/environ")
	assert.ErrorIs(t, err, ErrDenied)
}
