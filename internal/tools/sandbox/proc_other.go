//go:build !unix

package sandbox

import "os/exec"

// setProcessGroup is a no-op where POSIX process groups do not exist;
// exec.CommandContext's default Cancel (kill the direct child) and
// WaitDelay still apply.
func setProcessGroup(*exec.Cmd) {}
