package sandbox

import (
	"context"
	"os/exec"
)

// GVisor wraps subprocess execution in `runsc do` — user-space
// syscall interception for stronger isolation than a plain container
// at the cost of ~15% latency overhead. Intended for hosts that
// already install runsc (Modal, Northflank, gVisor-enabled kubelets).
//
// STATUS: scaffold. The runtime binary lookup + argument shaping is
// wired, but end-to-end tests + full mount plumbing are follow-up
// work. Callers that instantiate this today MUST verify their runsc
// is a recent build (>= release-20240226); older releases don't
// support `runsc do` and will fail at first Run with a clear error.
type GVisor struct {
	// Binary overrides the runsc executable path. Empty resolves via $PATH.
	Binary string
}

func newGVisor() Backend { return &GVisor{} }

// Kind returns "gvisor".
func (*GVisor) Kind() string { return "gvisor" }

// Run resolves runsc, prepends its arguments, then delegates to the
// [None] backend for the actual exec. Returns [ErrUnavailable] when
// runsc isn't on $PATH.
func (g *GVisor) Run(ctx context.Context, cmd Command) (Result, error) {
	bin := g.Binary
	if bin == "" {
		bin = "runsc"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return Result{}, ErrUnavailable
	}
	// Rewrap: runsc do <args...> <cmd> <cmd-args...>
	//
	// TODO(sandbox.gvisor): --network=none for by-default no-egress,
	// --root=<per-invocation-tmpdir> for isolated state, --rootless
	// for user-namespace mode. See docs/security/sandbox.md for the
	// full argument set we plan to standardise on.
	wrapped := Command{
		Path:  bin,
		Args:  append([]string{"do"}, append([]string{cmd.Path}, cmd.Args...)...),
		Stdin: cmd.Stdin,
		Env:   cmd.Env,
		Dir:   cmd.Dir,
	}
	return (&None{}).Run(ctx, wrapped)
}
