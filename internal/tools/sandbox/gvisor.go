package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GVisor wraps subprocess execution in `runsc do` — user-space
// syscall interception for stronger isolation than a plain container
// at the cost of ~15% latency overhead. Intended for hosts that
// already install runsc (Modal, Northflank, gVisor-enabled kubelets).
//
// Argv shape (translated from Policy):
//
//	runsc --rootless [--network=none] --root=<state dir> \
//	      do -force-overlay=true -root=<private rootfs> -cwd=<dir> \
//	         -volume=<path>:<path> ... -- <cmd...>
//
// `runsc do` defaults its root to the host's "/", so without -root a
// sandboxed command can read ~/.ssh and the daemon config. The backend
// instead builds a private root directory per invocation holding only
// mount points (and recreated symlinks) for Policy.Readonly — or the
// minimal system set [defaultReadonly] — and Policy.Writable; each is
// passed as `-volume`. $HOME, any ancestor of it and "/" are refused
// as mounts ([CheckFilesystemConfinement]). `-force-overlay=true`
// puts a memory overlay on the root and every mount
// (--overlay2=all:memory), so writes never reach the host — including
// writes to Policy.Writable paths, which are therefore ephemeral.
//
// The global --root names runsc's state directory, not the sandbox
// root; the `do` subcommand's -root is the container rootfs.
//
// Callers MUST use runsc >= release-20250611.0, the first release
// with `runsc do -volume`; older builds reject the flag and every Run
// fails closed.
type GVisor struct {
	// Binary overrides the runsc executable path. Empty resolves via $PATH.
	Binary string
	// Policy shapes the argv. Zero value uses safe defaults —
	// see [Policy] and [DefaultPolicy].
	Policy Policy
}

// Kind returns "gvisor".
func (*GVisor) Kind() string { return "gvisor" }

// gvisorMount is one path made visible inside the sandbox root:
// either bind-mounted (Link empty) or recreated as a symlink whose
// target is Link.
type gvisorMount struct {
	Path  string
	Link  string
	IsDir bool
}

// Run resolves runsc, checks the policy confines the filesystem,
// builds the per-invocation state dir and private rootfs, then
// delegates to the [None] backend for the actual exec. Returns
// [ErrUnavailable] when runsc isn't on $PATH. Everything created is
// removed after Run returns.
func (g *GVisor) Run(ctx context.Context, cmd Command) (Result, error) {
	bin := g.Binary
	if bin == "" {
		bin = "runsc"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return Result{}, ErrUnavailable
	}
	if err := CheckFilesystemConfinement(g.Policy); err != nil {
		return Result{}, fmt.Errorf("sandbox/gvisor: %w", err)
	}
	inv, cleanup, err := perInvocationTmpdir(g.Policy.TmpdirRoot, "rousseau-gvisor-")
	if err != nil {
		return Result{}, fmt.Errorf("sandbox/gvisor: tmpdir: %w", err)
	}
	defer cleanup()

	mounts := gvisorMounts(g.Policy)
	state, rootfs := filepath.Join(inv, "state"), filepath.Join(inv, "rootfs")
	if err := buildGVisorRootfs(rootfs, mounts); err != nil {
		return Result{}, fmt.Errorf("sandbox/gvisor: rootfs: %w", err)
	}
	if err := os.Mkdir(state, 0o700); err != nil {
		return Result{}, fmt.Errorf("sandbox/gvisor: state dir: %w", err)
	}

	// runsc's global flags come BEFORE `do`, the `do` flags after it;
	// the sandboxed argv follows a bare `--` so runsc doesn't try to
	// interpret sub-flags.
	args := gvisorArgs(g.Policy, state)
	args = append(args, "do")
	args = append(args, gvisorDoArgs(rootfs, gvisorCwd(cmd.Dir, mounts), mounts)...)
	args = append(args, "--", cmd.Path)
	args = append(args, cmd.Args...)

	wrapped := Command{
		Path:  bin,
		Args:  args,
		Stdin: cmd.Stdin,
		Env:   cmd.Env,
		Dir:   cmd.Dir,
	}
	return (&None{}).Run(ctx, wrapped)
}

// gvisorArgs builds the runsc global (pre-`do`) flag set from Policy.
// Kept separate from Run so tests can assert the exact flag order.
func gvisorArgs(p Policy, state string) []string {
	// --rootless matches the container's UserNS=keep-id: runsc uses
	// user namespaces instead of trying to unshare as root.
	args := []string{"--rootless"}
	if p.NoNetwork {
		args = append(args, "--network=none")
	}
	if state != "" {
		args = append(args, "--root="+state)
	}
	// runsc supports --total-memory via runsc-config only; the flag
	// is not on `runsc do`. Memory + CPU limits therefore surface
	// through the caller's cgroup (which the daemon container already
	// scopes). Documented in docs/security/sandbox.md.
	return args
}

// gvisorDoArgs builds the `runsc do` flag set: a forced overlay, the
// private rootfs, the working directory and one -volume per
// bind-mounted path. Symlink entries live in the rootfs and need no
// flag.
func gvisorDoArgs(rootfs, cwd string, mounts []gvisorMount) []string {
	args := []string{"-force-overlay=true", "-root=" + rootfs, "-cwd=" + cwd}
	for _, m := range mounts {
		if m.Link == "" {
			args = append(args, "-volume="+m.Path+":"+m.Path)
		}
	}
	return args
}

// gvisorMounts lists what the sandbox root exposes: Policy.Readonly
// (or the minimal system set) followed by Policy.Writable. A path
// that is a symlink on the host is recreated as the same symlink, so
// a merged-/usr `/bin -> usr/bin` resolves inside the sandbox.
func gvisorMounts(p Policy) []gvisorMount {
	paths := p.Readonly
	if len(paths) == 0 {
		paths = defaultReadonly(p.NoNetwork)
	}
	paths = append(append([]string(nil), paths...), p.Writable...)
	out := make([]gvisorMount, 0, len(paths))
	for _, path := range paths {
		m := gvisorMount{Path: filepath.Clean(path), IsDir: true}
		if fi, err := os.Lstat(path); err == nil {
			m.IsDir = fi.IsDir()
			if fi.Mode()&os.ModeSymlink != 0 {
				m.Link, _ = os.Readlink(path) //nolint:errcheck // Lstat just saw a symlink; an empty target falls back to a bind mount
			}
		}
		out = append(out, m)
	}
	return out
}

// buildGVisorRootfs creates the private root: the directories runsc
// mounts its own filesystems over, then a mount point (or symlink)
// for every entry.
func buildGVisorRootfs(rootfs string, mounts []gvisorMount) error {
	for _, d := range []string{"dev", "proc", "sys", "tmp"} {
		if err := os.MkdirAll(filepath.Join(rootfs, d), 0o755); err != nil {
			return err
		}
	}
	for _, m := range mounts {
		if err := makeMountPoint(rootfs, m); err != nil {
			return err
		}
	}
	return nil
}

func makeMountPoint(rootfs string, m gvisorMount) error {
	dst := filepath.Join(rootfs, m.Path)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	switch {
	case m.Link != "":
		return os.Symlink(m.Link, dst)
	case m.IsDir:
		return os.MkdirAll(dst, 0o755)
	default:
		return os.WriteFile(dst, nil, 0o600)
	}
}

// gvisorCwd keeps the caller's working directory when it is inside a
// bind-mounted path; anything else (including the empty default)
// becomes "/" so runsc never needs a host path the root lacks.
func gvisorCwd(dir string, mounts []gvisorMount) string {
	if dir == "" {
		return "/"
	}
	dir = filepath.Clean(dir)
	for _, m := range mounts {
		if m.Link == "" && m.IsDir && pathWithin(dir, m.Path) {
			return dir
		}
	}
	return "/"
}

// CheckFilesystemConfinement reports whether a Policy keeps the
// gVisor sandbox's filesystem away from the operator's secrets. It
// refuses any Readonly or Writable path that is "/", $HOME or an
// ancestor of $HOME (each compared both as written and with symlinks
// resolved), and any path containing ':' — `runsc do -volume` splits
// SRC:DST on the first colon. Paths below $HOME, such as the default
// workspace, are allowed.
func CheckFilesystemConfinement(p Policy) error {
	home, _ := os.UserHomeDir() //nolint:errcheck // no home: only "/" is checked
	for _, path := range append(append([]string(nil), p.Readonly...), p.Writable...) {
		if strings.Contains(path, ":") {
			return fmt.Errorf("mount %q contains ':', which runsc -volume cannot express", path)
		}
		if exposesHome(path, home) {
			return fmt.Errorf("%w: mount %q is \"/\", $HOME or an ancestor of $HOME", errExposesHome, path)
		}
	}
	return nil
}

var errExposesHome = errors.New("gvisor mount would expose $HOME")

func exposesHome(path, home string) bool {
	for _, p := range []string{filepath.Clean(path), resolved(path)} {
		if p == "/" {
			return true
		}
		if home == "" {
			continue
		}
		for _, h := range []string{filepath.Clean(home), resolved(home)} {
			if pathWithin(h, p) {
				return true
			}
		}
	}
	return false
}

// resolved returns path with symlinks evaluated, or the cleaned path
// when it does not exist.
func resolved(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return filepath.Clean(path)
}

// pathWithin reports whether path is dir or lies below it.
func pathWithin(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// perInvocationTmpdir creates a fresh directory under parent (or
// os.TempDir() when empty) with the given prefix. Returns the dir
// path and a cleanup func the caller MUST call.
func perInvocationTmpdir(parent, prefix string) (string, func(), error) {
	dir, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return "", func() {}, err
	}
	// The best-effort cleanup swallows removal errors on purpose —
	// a per-invocation tmpdir failing to remove is a leak, not a
	// correctness bug, and surfacing it to the caller would mask
	// the actual command result.
	return dir, func() { _ = os.RemoveAll(dir) }, nil //nolint:errcheck // best-effort cleanup
}
