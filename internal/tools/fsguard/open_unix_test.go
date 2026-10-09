//go:build unix

package fsguard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swapToOutside replaces directory dir with a symlink to outside,
// simulating a swap that lands between Resolve and the file operation.
func swapToOutside(t *testing.T, dir, outside string) {
	t.Helper()
	require.NoError(t, os.Rename(dir, dir+".moved"))
	require.NoError(t, os.Symlink(outside, dir))
}

// A directory swapped for a symlink after Resolve cannot make a read
// leave the workspace.
func TestOpenRead_SwappedSymlinkCannotEscapeRoot(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "sub"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "sub", "f"), []byte("inside"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "f"), []byte("OUTSIDE-SECRET"), 0o600))
	g, err := New(ws, nil)
	require.NoError(t, err)
	resolved, err := g.Resolve(filepath.Join(ws, "sub", "f"))
	require.NoError(t, err)

	swapToOutside(t, filepath.Join(ws, "sub"), outside)
	f, err := g.OpenRead(resolved, os.O_RDONLY)
	if err == nil {
		b := make([]byte, 64)
		n, _ := f.Read(b) //nolint:errcheck // only the content matters
		_ = f.Close()     //nolint:errcheck // test
		t.Fatalf("read escaped the root and returned %q", b[:n])
	}
}

// The same swap cannot make a write land outside the workspace.
func TestWriteFile_SwappedSymlinkCannotEscapeRoot(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "sub"), 0o700))
	target := filepath.Join(outside, "authorized_keys")
	require.NoError(t, os.WriteFile(target, []byte("original"), 0o600))
	g, err := New(ws, nil)
	require.NoError(t, err)
	resolved, err := g.ResolveForWrite(filepath.Join(ws, "sub", "authorized_keys"))
	require.NoError(t, err)

	swapToOutside(t, filepath.Join(ws, "sub"), outside)
	assert.Error(t, g.WriteFile(resolved, []byte("ssh-ed25519 ATTACKER"), 0o644))
	got, err := os.ReadFile(target) //nolint:gosec // test path
	require.NoError(t, err)
	assert.Equal(t, "original", string(got), "the outside file is untouched")
}

func TestWriteFile_AtomicKeepsPermsAndRefusesReadOnly(t *testing.T) {
	ws := t.TempDir()
	g, err := New(ws, nil)
	require.NoError(t, err)
	// WriteFile takes a resolved path, as write and edit pass it. On macOS
	// t.TempDir is under /var, a symlink to /private/var, so the raw path
	// is not beneath the resolved root.
	p, err := g.ResolveForWrite(filepath.Join(ws, "a", "b", "file"))
	require.NoError(t, err)
	require.NoError(t, g.WriteFile(p, []byte("one"), 0o644))
	require.NoError(t, os.Chmod(p, 0o600))
	require.NoError(t, g.WriteFile(p, []byte("two"), 0o644))
	info, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "existing permissions are kept")
	got, _ := os.ReadFile(p) //nolint:errcheck,gosec // test path
	assert.Equal(t, "two", string(got))
	entries, _ := os.ReadDir(filepath.Dir(p)) //nolint:errcheck // test
	assert.Len(t, entries, 1, "no temp file left behind")

	require.NoError(t, os.Chmod(p, 0o400))
	assert.ErrorIs(t, g.WriteFile(p, []byte("three"), 0o644), os.ErrPermission)
}

// A missing daemon root is created on first write.
func TestWriteFile_CreatesMissingRoot(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "workspace")
	g, err := New(ws, nil)
	require.NoError(t, err)
	p, err := g.ResolveForWrite(filepath.Join(ws, "notes.md"))
	require.NoError(t, err)
	require.NoError(t, g.WriteFile(p, []byte("x"), 0o644))
	info, err := os.Stat(ws)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// Without a root (or with the "/" opt-out) writes still go through the
// atomic path.
func TestWriteFile_Unconfined(t *testing.T) {
	isolatedHome(t)
	dir := t.TempDir()
	g, err := New("", nil)
	require.NoError(t, err)
	p := filepath.Join(dir, "x", "y.txt")
	require.NoError(t, g.WriteFile(p, []byte("z"), 0o644))
	f, err := g.OpenRead(p, os.O_RDONLY)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}
