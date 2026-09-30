//go:build unix

package claudecli

import (
	"os/exec"
	"syscall"
	"time"
)

// killGrace is how long the claude process group gets between SIGTERM
// and SIGKILL once a turn is cancelled. A var so tests can shorten it.
var killGrace = 10 * time.Second

// setGracefulCancel runs the child in its own process group and, when
// the command's context is cancelled (turn timeout, /cancel, shutdown),
// sends SIGTERM to the whole group, then SIGKILL after killGrace.
//
// The group matters: claude runs tools as its own children (bash,
// MCP servers). exec.CommandContext's default SIGKILLs only the direct
// child, leaving grandchildren alive and holding the stdout pipe open,
// so the stream reader blocked until they exited on their own.
func setGracefulCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM) //nolint:errcheck // group may already be gone
		time.AfterFunc(killGrace, func() {
			_ = syscall.Kill(-pgid, syscall.SIGKILL) //nolint:errcheck // group may already be gone
		})
		return nil
	}
	cmd.WaitDelay = killGrace + 5*time.Second
}
