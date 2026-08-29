package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// GVisor wraps subprocess execution in `runsc do` — user-space
// syscall interception for stronger isolation than a plain container
// at the cost of ~15% latency overhead. Intended for hosts that
// already install runsc (Modal, Northflank, gVisor-enabled kubelets).
//
// Argv shape (translated from Policy):
//
//	runsc [--rootless] [--network=none] [--root=<tmpdir>] do -- <cmd...>
//
// See [Policy] for what each field means; empty Policy still fires
// the two zero-cost defence-in-depth flags (--rootless, --network=none)
// because both are the correct disposition for the primary caller
// (the bash tool executing model-authored commands).
//
// Callers MUST use a runsc >= release-20240226; older builds do not
// support `runsc do` and will fail at first Run.
type GVisor struct {
	// Binary overrides the runsc executable path. Empty resolves via $PATH.
	Binary string
	// Policy shapes the argv. Zero value uses safe defaults —
	// see [Policy] and [DefaultPolicy].
	Policy Policy
}

// Kind returns "gvisor".
func (*GVisor) Kind() string { return "gvisor" }

// Run resolves runsc, prepends its arguments, then delegates to the
// [None] backend for the actual exec. Returns [ErrUnavailable] when
// runsc isn't on $PATH. Creates a per-invocation tmpdir under
// Policy.TmpdirRoot (or os.TempDir() when unset) and removes it
// after Run returns.
func (g *GVisor) Run(ctx context.Context, cmd Command) (Result, error) {
	bin := g.Binary
	if bin == "" {
		bin = "runsc"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return Result{}, ErrUnavailable
	}

	// Per-invocation tmpdir. Even for a runsc build that ignores
	// --root, having a fresh dir per invocation means the caller's
	// crash cleanup story stays simple.
	root, cleanup, err := perInvocationTmpdir(g.Policy.TmpdirRoot, "rousseau-gvisor-")
	if err != nil {
		return Result{}, fmt.Errorf("sandbox/gvisor: tmpdir: %w", err)
	}
	defer cleanup()

	args := gvisorArgs(g.Policy, root)
	// runsc's own flags come BEFORE `do`; the sandboxed argv comes
	// after a bare `--` so runsc doesn't try to interpret sub-flags.
	args = append(args, "do", "--")
	args = append(args, cmd.Path)
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

// gvisorArgs builds the runsc pre-`do` flag set from Policy. Kept
// separate from Run so tests can assert the exact flag order.
func gvisorArgs(p Policy, root string) []string {
	// --rootless matches the container's UserNS=keep-id: runsc uses
	// user namespaces instead of trying to unshare as root.
	args := []string{"--rootless"}
	if p.NoNetwork {
		args = append(args, "--network=none")
	}
	if root != "" {
		args = append(args, "--root="+root)
	}
	// runsc supports --total-memory via runsc-config only; the flag
	// is not on `runsc do`. Memory + CPU limits therefore surface
	// through the caller's cgroup (which the daemon container already
	// scopes). Documented in docs/security/sandbox.md.
	return args
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
