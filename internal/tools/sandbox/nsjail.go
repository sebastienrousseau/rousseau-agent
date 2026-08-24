package sandbox

import (
	"context"
	"os/exec"
)

// NSJail wraps subprocess execution in `nsjail` — kernel-namespace
// isolation without the syscall-interception overhead of gVisor.
// Cheaper and simpler than gVisor; weaker (a kernel exploit can
// escape). Suitable for lightly-trusted models running local tests.
//
// STATUS: scaffold. Wiring is present; the specific argv (mount
// bindings, resource limits, seccomp filter selection) is left as
// follow-up so we can pin against a specific nsjail release.
type NSJail struct {
	Binary string
}

func newNSJail() Backend { return &NSJail{} }

func (*NSJail) Kind() string { return "nsjail" }

func (n *NSJail) Run(ctx context.Context, cmd Command) (Result, error) {
	bin := n.Binary
	if bin == "" {
		bin = "nsjail"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return Result{}, ErrUnavailable
	}
	// TODO(sandbox.nsjail): pass --mode o (once), --disable_clone_newuser=false,
	// --time_limit, --rlimit_as, --rlimit_cpu, --bindmount from a
	// tmpdir template. Full argv landing in a follow-up ticket.
	wrapped := Command{
		Path:  bin,
		Args:  append([]string{"--quiet", "--", cmd.Path}, cmd.Args...),
		Stdin: cmd.Stdin,
		Env:   cmd.Env,
		Dir:   cmd.Dir,
	}
	return (&None{}).Run(ctx, wrapped)
}
