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
//     proof against a kernel exploit chain.
//   - gvisor:      protect against kernel-exploit-carrying commands
//     at the cost of syscall overhead. Modal/Northflank
//     tier.
//   - firecracker: microVM per invocation. Cold-start ~200ms,
//     strongest isolation short of a dedicated machine.
package sandbox

import (
	"context"
	"errors"
	"time"
)

// Policy is the operator-supplied guardrail set that shapes each
// backend's argv. Zero-value Policy is safe: NoNetwork defaults on
// (deny egress), other knobs default to backend-native "no limit"
// so an empty policy on gvisor is the pre-policy behaviour minus
// network — matching the "protect against accidental exfiltration"
// baseline in docs/security/sandbox.md.
//
// Backends translate Policy fields into their own flag surface:
//
//   - gvisor  → --network=none, --rootless, --root=<per-invocation-tmpdir>
//   - nsjail  → --mode o (once), --disable_clone_newuser=false,
//     --time_limit, --rlimit_as, --rlimit_cpu,
//     --bindmount / --bindmount_ro
//   - none    → policy is advisory; None does not enforce anything.
//     Included in the interface so callers do not need to
//     branch on backend kind when supplying a policy.
type Policy struct {
	// NoNetwork blocks all outbound traffic from the sandboxed
	// process. Enforced by the backend's native network isolation
	// (gvisor --network=none; nsjail creates a fresh netns).
	// Defaults on. Set false only when the sandboxed command needs
	// network by design (e.g. a HTTP-driven tool the operator has
	// explicitly whitelisted).
	NoNetwork bool
	// TmpdirRoot is the parent directory under which each invocation
	// gets a fresh sub-directory. Empty uses os.TempDir(). The
	// sub-directory is created before Run and removed after, so a
	// crashing subprocess cannot leak state across invocations.
	TmpdirRoot string
	// Wallclock caps the subprocess elapsed time. Zero uses the
	// context's deadline (the exec.CommandContext already enforces
	// that). Nonzero is passed through to the backend as its own
	// timeout for defence in depth against a runaway that ignores
	// SIGTERM.
	Wallclock time.Duration
	// CPUSeconds caps consumed CPU time. Backends that support it
	// (nsjail --rlimit_cpu) translate directly. Zero disables.
	CPUSeconds int
	// MemoryBytes caps address-space size. Backends translate to
	// --rlimit_as (nsjail) or --memory (gvisor via runsc-config).
	// Zero disables.
	MemoryBytes int64
	// Readonly is a list of host paths bind-mounted RO into the
	// sandbox at the same path. Backends that don't isolate the
	// filesystem (None) ignore this. Empty falls back to the
	// backend's default read-only set (typically /usr, /lib, /bin).
	Readonly []string
	// Writable is a list of host paths bind-mounted RW into the
	// sandbox at the same path. The per-invocation tmpdir is always
	// writable (added by the backend); Writable adds to that.
	Writable []string
}

// DefaultPolicy returns the shipped "safe-by-default" policy: no
// network, no limits beyond the caller's ctx deadline, no extra
// bindmounts. Suitable for `bash`-tool-style commands that read and
// write within the workspace.
func DefaultPolicy() Policy {
	return Policy{NoNetwork: true}
}

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
//
// Kept for callers that don't need the policy surface. New callers
// should prefer [NewWithPolicy] so operator-configured guardrails
// (network isolation, tmpdir root, resource limits) can be threaded
// through to the backend.
//
// The resulting backend is initialised with [DefaultPolicy] — a
// safe disposition (NoNetwork on, no extra bindmounts, no limits
// beyond the caller's context deadline). Callers who want an
// escape hatch (e.g. tools that legitimately need network) MUST
// use [NewWithPolicy] with an explicit Policy so a code review can
// spot the deviation.
func New(kind string) (Backend, error) {
	return NewWithPolicy(kind, DefaultPolicy())
}

// NewWithPolicy returns the backend named kind, configured with the
// given policy. Empty kind resolves to [None]; unknown kinds return
// an error. See [Policy] for what each field means and which
// backend honours it.
func NewWithPolicy(kind string, pol Policy) (Backend, error) {
	switch kind {
	case "", "none":
		return &None{}, nil
	case "gvisor":
		return &GVisor{Policy: pol}, nil
	case "nsjail":
		return &NSJail{Policy: pol}, nil
	case "firecracker":
		return newFirecracker(), nil
	default:
		return nil, errors.New("sandbox: unknown backend " + kind)
	}
}
