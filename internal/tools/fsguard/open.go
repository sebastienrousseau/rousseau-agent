package fsguard

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// confined reports whether file operations must go through an
// os.Root: a workspace root is set and it is not the explicit "/"
// opt-out.
func (g *Guard) confined() bool {
	return g.root != "" && g.root != string(filepath.Separator)
}

// rel returns resolved relative to the root, refusing anything that
// would leave it.
func (g *Guard) rel(resolved string) (string, error) {
	r, err := filepath.Rel(g.root, resolved)
	if err != nil || r == ".." || filepath.IsAbs(r) || len(r) > 2 && r[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("%w: %s (root %s)", ErrOutsideRoot, resolved, g.root)
	}
	return r, nil
}

// OpenRead opens resolved (a path returned by Resolve) with flag. With
// a workspace root the open goes through an os.Root, which resolves
// every component beneath the root and refuses a symlink that leads
// out of it, so swapping a link between Resolve and the open cannot
// escape the workspace.
func (g *Guard) OpenRead(resolved string, flag int) (*os.File, error) {
	if !g.confined() {
		return os.OpenFile(resolved, flag, 0) //nolint:gosec // path vetted by Resolve
	}
	r, err := os.OpenRoot(g.root)
	if err != nil {
		return nil, err
	}
	defer r.Close() //nolint:errcheck // the file outlives the root handle
	rel, err := g.rel(resolved)
	if err != nil {
		return nil, err
	}
	return r.OpenFile(rel, flag, 0)
}

// WriteFile writes data to resolved (a path returned by
// ResolveForWrite) atomically: a temp file in the same directory is
// written, synced and renamed over the target, so a reader never sees
// a half-written file and a symlink at the target is replaced, not
// followed. An existing file keeps its permissions; a new one gets
// perm. With a workspace root every step goes through an os.Root, and
// a missing root is created 0700.
func (g *Guard) WriteFile(resolved string, data []byte, perm os.FileMode) error {
	if !g.confined() {
		return writeAtomic(osFS{}, resolved, data, perm)
	}
	if err := os.MkdirAll(g.root, 0o700); err != nil {
		return err
	}
	r, err := os.OpenRoot(g.root)
	if err != nil {
		return err
	}
	defer r.Close() //nolint:errcheck // nothing outlives the write
	rel, err := g.rel(resolved)
	if err != nil {
		return err
	}
	return writeAtomic(rootFS{r}, rel, data, perm)
}

// writeFS is the subset of file operations writeAtomic needs, backed
// by either the plain OS or an os.Root.
type writeFS interface {
	MkdirAll(name string, perm os.FileMode) error
	Stat(name string) (os.FileInfo, error)
	OpenFile(name string, flag int, perm os.FileMode) (*os.File, error)
	Rename(oldname, newname string) error
	Remove(name string) error
}

type osFS struct{}

func (osFS) MkdirAll(n string, p os.FileMode) error { return os.MkdirAll(n, p) }
func (osFS) Stat(n string) (os.FileInfo, error)     { return os.Stat(n) }
func (osFS) OpenFile(n string, f int, p os.FileMode) (*os.File, error) {
	return os.OpenFile(n, f, p) //nolint:gosec // path vetted by ResolveForWrite
}
func (osFS) Rename(a, b string) error { return os.Rename(a, b) }
func (osFS) Remove(n string) error    { return os.Remove(n) }

type rootFS struct{ r *os.Root }

func (f rootFS) MkdirAll(n string, p os.FileMode) error { return f.r.MkdirAll(n, p) }
func (f rootFS) Stat(n string) (os.FileInfo, error)     { return f.r.Stat(n) }
func (f rootFS) OpenFile(n string, fl int, p os.FileMode) (*os.File, error) {
	return f.r.OpenFile(n, fl, p)
}
func (f rootFS) Rename(a, b string) error { return f.r.Rename(a, b) }
func (f rootFS) Remove(n string) error    { return f.r.Remove(n) }

func writeAtomic(fsys writeFS, name string, data []byte, perm os.FileMode) error {
	if dir := filepath.Dir(name); dir != "." {
		if err := fsys.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	perm, err := targetPerm(fsys, name, perm)
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".rousseau-"+randomSuffix())
	f, err := fsys.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if err := writeAndSync(f, data); err != nil {
		_ = fsys.Remove(tmp) //nolint:errcheck // already failing
		return err
	}
	if err := fsys.Rename(tmp, name); err != nil {
		_ = fsys.Remove(tmp) //nolint:errcheck // already failing
		return err
	}
	return nil
}

// targetPerm returns the permissions the new file takes: an existing
// file's own, or perm for a new one. A read-only target is refused,
// as a plain open for writing would be, rather than replaced by the
// rename.
func targetPerm(fsys writeFS, name string, perm os.FileMode) (os.FileMode, error) {
	info, err := fsys.Stat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return perm, nil
	case err != nil:
		return 0, err
	case !info.Mode().IsRegular():
		return 0, fmt.Errorf("%s: not a regular file", name)
	case info.Mode().Perm()&0o200 == 0:
		return 0, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return info.Mode().Perm(), nil
}

func writeAndSync(f *os.File, data []byte) error {
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	return errors.Join(werr, serr, cerr)
}

func randomSuffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on supported platforms
	return hex.EncodeToString(b[:])
}
