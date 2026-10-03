//go:build unix

package cli

import "syscall"

// restrictUmask sets the process umask to 077 so every file and
// directory rousseau creates (session store, WhatsApp device keys and
// message secrets, SQLite WAL files, sockets) is private to the user.
// SQLite and whatsmeow create files themselves with 0644, so a umask
// is the one place that covers them all. Returns a restore func.
func restrictUmask() func() {
	old := syscall.Umask(0o077)
	return func() { syscall.Umask(old) }
}
