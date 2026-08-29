package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
)

// NSJail wraps subprocess execution in `nsjail` — kernel-namespace
// isolation without the syscall-interception overhead of gVisor.
// Cheaper and simpler than gVisor; weaker (a kernel exploit can
// escape). Suitable for lightly-trusted models running local tests.
//
// Argv shape (translated from Policy):
//
//	nsjail --quiet --mode o --disable_clone_newuser=false \
//	       [--disable_clone_newnet=false --disable_proc] \
//	       [--time_limit=<sec>] [--rlimit_cpu=<sec>] [--rlimit_as=<MiB>] \
//	       [--bindmount=<scratch>:<scratch>] \
//	       [--bindmount_ro=/x:/x ...] [--bindmount=/y:/y ...] \
//	       -- <cmd...>
//
// The three always-on flags (`--quiet --mode o
// --disable_clone_newuser=false`) keep compatibility with the
// rootless container the daemon typically runs inside: we already
// unshare into a user namespace, and nsjail's default of ALSO
// unsharing would double-nest and fail.
type NSJail struct {
	// Binary overrides the nsjail executable path. Empty resolves via $PATH.
	Binary string
	// Policy shapes the argv. Zero value uses safe defaults —
	// see [Policy] and [DefaultPolicy].
	Policy Policy
}

// Kind returns "nsjail".
func (*NSJail) Kind() string { return "nsjail" }

// Run executes cmd inside an nsjail sandbox, or returns
// [ErrUnavailable] when nsjail isn't on $PATH. Creates a per-
// invocation tmpdir and adds it to the writable bindmount set so
// the sandboxed process has somewhere to scratch.
func (n *NSJail) Run(ctx context.Context, cmd Command) (Result, error) {
	bin := n.Binary
	if bin == "" {
		bin = "nsjail"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return Result{}, ErrUnavailable
	}

	scratch, cleanup, err := perInvocationTmpdir(n.Policy.TmpdirRoot, "rousseau-nsjail-")
	if err != nil {
		return Result{}, fmt.Errorf("sandbox/nsjail: tmpdir: %w", err)
	}
	defer cleanup()

	args := nsjailArgs(n.Policy, scratch)
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

// nsjailArgs builds the nsjail pre-`--` flag set from Policy. Kept
// separate from Run so tests can assert the exact flag order.
func nsjailArgs(p Policy, scratch string) []string {
	// The three always-on flags — see the type doc for why.
	args := []string{"--quiet", "--mode", "o", "--disable_clone_newuser=false"}
	if p.NoNetwork {
		// nsjail creates a fresh network namespace by default when
		// clone_newnet is enabled; --disable_proc keeps /proc from
		// leaking host process listings.
		args = append(args, "--disable_clone_newnet=false", "--disable_proc")
	}
	if p.Wallclock > 0 {
		args = append(args, "--time_limit", strconv.Itoa(int(p.Wallclock.Seconds())))
	}
	if p.CPUSeconds > 0 {
		args = append(args, "--rlimit_cpu", strconv.Itoa(p.CPUSeconds))
	}
	if p.MemoryBytes > 0 {
		// nsjail --rlimit_as is in MiB per its manpage.
		mib := p.MemoryBytes / (1024 * 1024)
		if mib < 1 {
			mib = 1
		}
		args = append(args, "--rlimit_as", strconv.FormatInt(mib, 10))
	}
	// The per-invocation scratch dir is always writable so the
	// process has somewhere to scratch. Callers who want a specific
	// workspace layout add more via Policy.Writable / Policy.Readonly.
	if scratch != "" {
		args = append(args, "--bindmount", scratch+":"+scratch)
	}
	for _, ro := range p.Readonly {
		args = append(args, "--bindmount_ro", ro+":"+ro)
	}
	for _, rw := range p.Writable {
		args = append(args, "--bindmount", rw+":"+rw)
	}
	return args
}
