// Package sandbox defines the isolation boundary the [bash] tool
// (and, in the future, any other subprocess-invoking tool) uses to
// contain untrusted-model-driven commands. It is an abstraction over
// several possible backends:
//
//   - [None] — direct exec, no isolation (matches pre-sandbox
//     behaviour; the default so nothing breaks on upgrade).
//   - `gvisor` — runsc-wrapped exec with a user-namespaced fs view.
//     Requires runsc on $PATH. Implementation is scaffolded in
//     gvisor.go with a build tag; the runtime is a follow-up ticket.
//   - `firecracker` — pooled microVM per-invocation. Not scaffolded
//     yet; documented in docs/security/sandbox.md as the top-tier
//     option for hostile-code use cases.
//   - `nsjail` — kernel-namespace jail via nsjail binary. Middle
//     ground: cheaper than gVisor, stronger than plain container.
//     Scaffolded for symmetry, follow-up ticket for runtime.
//
// Backends do NOT provide network isolation by themselves — the
// caller is expected to run the daemon inside a network namespace
// (rousseau's own container spec does this via pasta) so the sandbox
// only needs to bound cpu/memory/wallclock/filesystem.
//
// # Trust model per backend
//
// See [docs/security/sandbox.md](../../../docs/security/sandbox.md)
// for a full write-up. Short version:
//
//   - None:        trust the agent implicitly. Zero isolation.
//   - nsjail:      protect the host from accidental writes; not
//                  proof against a kernel exploit chain.
//   - gvisor:      protect against kernel-exploit-carrying commands
//                  at the cost of syscall overhead. Modal/Northflank
//                  tier.
//   - firecracker: microVM per invocation. Cold-start ~200ms,
//                  strongest isolation short of a dedicated machine.
package sandbox

import (
	"context"
	"errors"
)

// Backend is the sandboxing strategy for a single subprocess
// invocation.
type Backend interface {
	// Kind returns the backend identifier ("none", "gvisor", …).
	// Callers use this for metrics + logging labels; nothing in the
	// code path branches on it.
	Kind() string
	// Run executes cmd inside the sandbox. The Command may be
	// modified in place (typically by prepending the sandbox
	// executable and its flags) — callers should treat cmd as
	// consumed after Run returns.
	Run(ctx context.Context, cmd Command) (Result, error)
}

// Command describes what to execute. Fields deliberately minimal —
// enough to model the current bash tool without dragging in every
// exec.Cmd knob.
type Command struct {
	// Path is the executable to run (typically "/bin/sh").
	Path string
	// Args is the argv (excluding Path — matches exec.Cmd.Args
	// convention where Args[0] is a repeat of Path).
	Args []string
	// Stdin, when set, is fed to the subprocess.
	Stdin []byte
	// Env is passed verbatim to the subprocess. Nil inherits the
	// parent env (backends that isolate the environment may reject
	// this or scrub sensitive vars).
	Env []string
	// Dir is the working directory. Empty inherits the parent's.
	// Backends that run in a jailed filesystem MUST make this path
	// reachable inside the jail.
	Dir string
}

// Result is what Run returns to the caller.
type Result struct {
	// CombinedOutput is stdout+stderr merged, matching what the
	// pre-sandbox bash tool returned.
	CombinedOutput string
	// ExitCode is the subprocess exit code. Zero on success.
	ExitCode int
}

// ErrUnavailable is returned when a backend that requires an external
// binary (runsc, firecracker, nsjail) doesn't find it on $PATH. The
// caller is expected to fall back to the None backend and log a WARN.
var ErrUnavailable = errors.New("sandbox: backend runtime is not installed")

// New returns the backend named kind. Empty string or "none" returns
// [None]. Unknown kinds return an error. Backends that require an
// external binary fail at first Run, not at New, so daemons can be
// configured to prefer a backend without every host having it.
func New(kind string) (Backend, error) {
	switch kind {
	case "", "none":
		return &None{}, nil
	case "gvisor":
		return newGVisor(), nil
	case "nsjail":
		return newNSJail(), nil
	case "firecracker":
		return newFirecracker(), nil
	default:
		return nil, errors.New("sandbox: unknown backend " + kind)
	}
}
