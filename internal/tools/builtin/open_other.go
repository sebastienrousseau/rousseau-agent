//go:build windows

package builtin

import "os"

// openFlags: Windows has no O_NOFOLLOW/O_NONBLOCK; the regular-file
// check after open still refuses anything else.
const openFlags = os.O_RDONLY
