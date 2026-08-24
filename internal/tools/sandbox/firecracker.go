package sandbox

import (
	"context"
	"errors"
)

// Firecracker is the strongest-isolation backend: each invocation
// runs in a pool-managed microVM with its own kernel.
//
// STATUS: scaffold only. Firecracker requires substantial extra
// machinery — a pool of pre-warmed VMs, a rootfs image, kernel
// image, jailer configuration, per-invocation vsock plumbing — that
// isn't ready to ship yet. Instantiating this backend at runtime
// returns ErrUnavailable so operators get a clear signal.
//
// The type + constructor are in place so [New] can return a Backend
// interface value uniformly and so the CI keeps this compiled while
// the runtime lands.
type Firecracker struct{}

func newFirecracker() Backend { return &Firecracker{} }

func (*Firecracker) Kind() string { return "firecracker" }

// Run returns ErrUnavailable — the pooled-microVM runtime is a
// separate ticket (see docs/security/sandbox.md).
func (*Firecracker) Run(_ context.Context, _ Command) (Result, error) {
	return Result{}, errors.Join(ErrUnavailable, errors.New("sandbox: firecracker runtime not implemented — track docs/security/sandbox.md"))
}
