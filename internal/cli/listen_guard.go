package cli

// Bind/TLS review shared by the daemon's network listeners (A2A,
// SCIM). Bearer tokens and directory data must not cross a network in
// clear text by default: a plaintext listener is allowed on loopback
// only, unless the operator opts in explicitly.

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
)

// errTLSPairIncomplete is returned when only one of the certificate
// and key paths is set.
var errTLSPairIncomplete = errors.New("tls_cert_file and tls_key_file must be set together")

// checkListenTLS validates a listen address against its TLS settings.
// A non-loopback bind (including ":port" and the unspecified
// addresses) without a certificate is refused unless allowPlaintext
// is set.
func checkListenTLS(addr, certFile, keyFile string, allowPlaintext bool) error {
	if (certFile == "") != (keyFile == "") {
		return errTLSPairIncomplete
	}
	if certFile != "" || allowPlaintext || isLoopbackListen(addr) {
		return nil
	}
	return fmt.Errorf("listen %q is not a loopback address and TLS is not configured; "+
		"set tls_cert_file and tls_key_file, bind 127.0.0.1, or set allow_plaintext: true", addr)
}

// isLoopbackListen reports whether addr binds only a loopback
// interface. A hostname other than "localhost" is not trusted to
// resolve to loopback.
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
