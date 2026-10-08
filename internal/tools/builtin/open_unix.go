//go:build !windows

package builtin

import (
	"os"
	"syscall"
)

// openFlags never follow a final symlink and never block on a FIFO or
// device: O_NONBLOCK makes open return at once, and the caller then
// refuses anything that is not a regular file.
const openFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
