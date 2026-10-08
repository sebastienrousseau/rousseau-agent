package fsguard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failFS wraps the OS and fails the named step.
type failFS struct {
	osFS
	fail    string
	removed []string
}

var errInjected = errors.New("injected")

func (f *failFS) MkdirAll(n string, p os.FileMode) error {
	if f.fail == "mkdir" {
		return errInjected
	}
	return f.osFS.MkdirAll(n, p)
}

func (f *failFS) Stat(n string) (os.FileInfo, error) {
	if f.fail == "stat" {
		return nil, errInjected
	}
	return f.osFS.Stat(n)
}

func (f *failFS) OpenFile(n string, fl int, p os.FileMode) (*os.File, error) {
	switch f.fail {
	case "open":
		return nil, errInjected
	case "write":
		// A read-only handle makes the write fail.
		g, err := f.osFS.OpenFile(n, fl, p)
		if err != nil {
			return nil, err
		}
		if cerr := g.Close(); cerr != nil {
			return nil, cerr
		}
		return os.Open(n) //nolint:gosec // test temp path
	}
	return f.osFS.OpenFile(n, fl, p)
}

func (f *failFS) Rename(a, b string) error {
	if f.fail == "rename" {
		return errInjected
	}
	return f.osFS.Rename(a, b)
}

func (f *failFS) Remove(n string) error {
	f.removed = append(f.removed, n)
	return f.osFS.Remove(n)
}

func TestWriteAtomic_StepFailures(t *testing.T) {
	for _, step := range []string{"mkdir", "stat", "open", "write", "rename"} {
		t.Run(step, func(t *testing.T) {
			dir := t.TempDir()
			fsys := &failFS{fail: step}
			err := writeAtomic(fsys, filepath.Join(dir, "sub", "f.txt"), []byte("x"), 0o600)
			require.Error(t, err)
			if step == "write" || step == "rename" {
				assert.Len(t, fsys.removed, 1, "the temp file is removed on failure")
				entries, rerr := os.ReadDir(filepath.Join(dir, "sub"))
				require.NoError(t, rerr)
				assert.Empty(t, entries, "no temp file is left behind")
			}
		})
	}
}

func TestTargetPerm(t *testing.T) {
	dir := t.TempDir()
	_, err := targetPerm(osFS{}, dir, 0o600)
	require.ErrorContains(t, err, "not a regular file")

	ro := filepath.Join(dir, "ro")
	require.NoError(t, os.WriteFile(ro, nil, 0o400))
	_, err = targetPerm(osFS{}, ro, 0o600)
	require.ErrorIs(t, err, fs.ErrPermission)

	rw := filepath.Join(dir, "rw")
	require.NoError(t, os.WriteFile(rw, nil, 0o640))
	perm, err := targetPerm(osFS{}, rw, 0o600)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), perm, "an existing file keeps its permissions")
}

func TestGuard_UnconfinedOpenAndWrite(t *testing.T) {
	g, err := New("/", nil)
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "a.txt")
	require.NoError(t, g.WriteFile(p, []byte("hi"), 0o600))
	f, err := g.OpenRead(p, os.O_RDONLY)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func TestGuard_ConfinedRefusesPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	g, err := New(root, nil)
	require.NoError(t, err)
	outside := filepath.Join(filepath.Dir(root), "elsewhere.txt")
	_, err = g.OpenRead(outside, os.O_RDONLY)
	require.ErrorIs(t, err, ErrOutsideRoot)
	require.ErrorIs(t, g.WriteFile(outside, nil, 0o600), ErrOutsideRoot)
	_, err = g.rel(filepath.Dir(root))
	require.ErrorIs(t, err, ErrOutsideRoot)
}

func TestGuard_ConfinedRootUnavailable(t *testing.T) {
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	g := &Guard{root: file}
	_, err := g.OpenRead(filepath.Join(file, "x"), os.O_RDONLY)
	require.Error(t, err, "a root that is not a directory cannot be opened")
	require.Error(t, g.WriteFile(filepath.Join(file, "x"), nil, 0o600))
}

func TestFS_Remove(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x")
	require.NoError(t, os.WriteFile(p, nil, 0o600))
	require.NoError(t, osFS{}.Remove(p))

	r, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer r.Close() //nolint:errcheck // test
	require.NoError(t, os.WriteFile(p, nil, 0o600))
	require.NoError(t, rootFS{r}.Remove("x"))
	_, err = os.Stat(p)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}
