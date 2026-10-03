//go:build !unix

package claudecli

import (
	"os/exec"
	"time"
)

var killGrace = 10 * time.Second

// setGracefulCancel keeps exec.CommandContext's kill-on-cancel on
// platforms without process groups, and bounds how long Wait waits
// for pipes held by orphaned grandchildren.
func setGracefulCancel(cmd *exec.Cmd) {
	cmd.WaitDelay = killGrace
}
