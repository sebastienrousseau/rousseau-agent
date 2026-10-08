package a2a

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlockedAddress is returned (wrapped) when the default fetch
// client refuses to connect to a non-public address. Artifact URIs
// come from peers, so a fetch must not become a way for a peer to
// reach the daemon's own network: loopback, RFC 1918, link-local
// (including the 169.254.169.254 cloud metadata endpoint), CGNAT,
// IPv6 unique-local, unspecified and multicast addresses are refused.
var ErrBlockedAddress = errors.New("a2a: fetch to a non-public address refused")

// blockedPrefixes are the ranges that IsGlobalUnicast/IsPrivate do
// not already exclude.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network" (RFC 1122)
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT (RFC 6598), incl. 100.100.100.200 metadata
}

// isPublicAddr reports whether ip is a public unicast address the
// fetcher may dial.
func isPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// guardedDialControl is the net.Dialer Control hook for the default
// fetch transport. It runs after DNS resolution, on the literal
// address being dialled, so a hostname that resolves (or re-resolves)
// to an internal address is refused too. Unparseable addresses fail
// closed.
func guardedDialControl(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparseable dial address %q", ErrBlockedAddress, address)
	}
	if !isPublicAddr(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, ap.Addr())
	}
	return nil
}

// newGuardedTransport clones the default transport with a dialer
// that applies guardedDialControl and with proxying disabled, so an
// environment proxy cannot dial the target on the fetcher's behalf
// and sidestep the address check.
func newGuardedTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	tr := base.Clone()
	tr.Proxy = nil
	tr.DialContext = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   guardedDialControl,
	}).DialContext
	return tr
}
