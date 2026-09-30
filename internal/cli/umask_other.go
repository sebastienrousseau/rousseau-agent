//go:build !unix

package cli

// restrictUmask is a no-op where there is no umask.
func restrictUmask() func() { return func() {} }
