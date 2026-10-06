package observability

import (
	"errors"
	"sync"
)

// ReadinessCheck reports whether the daemon can serve work. Nil error
// means ready.
type ReadinessCheck func() error

var (
	readyMu    sync.RWMutex
	readyCheck ReadinessCheck
)

// ErrNoReadinessCheck is returned by Ready until a transport registers
// its check: a daemon that has not started a transport is not ready.
var ErrNoReadinessCheck = errors.New("no transport has registered a readiness check")

// SetReadinessCheck installs the function /readyz consults. The
// transport command registers it alongside the heartbeat so the HTTP
// probe and the file-based `rousseau health` agree.
func SetReadinessCheck(fn ReadinessCheck) {
	readyMu.Lock()
	defer readyMu.Unlock()
	readyCheck = fn
}

// Ready evaluates the registered readiness check.
func Ready() error {
	readyMu.RLock()
	fn := readyCheck
	readyMu.RUnlock()
	if fn == nil {
		return ErrNoReadinessCheck
	}
	return fn()
}
