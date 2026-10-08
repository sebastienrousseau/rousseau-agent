package a2a

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardedDialControl: the dialer refuses loopback, RFC 1918,
// link-local (incl. cloud metadata), CGNAT, unique-local, unspecified
// and multicast addresses, including IPv4-mapped IPv6 forms, and
// allows public unicast.
func TestGuardedDialControl(t *testing.T) {
	blocked := []string{
		"127.0.0.1:80", "127.9.9.9:443", "[::1]:443",
		"10.0.0.1:443", "172.16.0.1:443", "172.31.255.255:443", "192.168.1.1:443",
		"169.254.169.254:80", "[fe80::1]:443",
		"100.64.0.1:443", "100.100.100.200:80",
		"[fc00::1]:443", "[fd00:ec2::254]:80",
		"0.0.0.0:80", "[::]:80",
		"224.0.0.1:80", "[ff02::1]:80",
		"[::ffff:127.0.0.1]:80", "[::ffff:169.254.169.254]:80",
	}
	for _, addr := range blocked {
		err := guardedDialControl("tcp", addr, nil)
		assert.ErrorIs(t, err, ErrBlockedAddress, addr)
	}
	for _, addr := range []string{"93.184.216.34:443", "8.8.8.8:53", "[2606:4700::1]:443", "100.128.0.1:443", "172.32.0.1:443"} {
		assert.NoError(t, guardedDialControl("tcp", addr, nil), addr)
	}
	assert.Error(t, guardedDialControl("tcp", "not-an-address", nil), "unparseable dial address fails closed")
}

// TestDefaultHTTPClient_NoProxy: the guarded transport dials the
// target itself, so an environment proxy cannot sidestep the check.
func TestDefaultHTTPClient_NoProxy(t *testing.T) {
	tr, ok := DefaultHTTPClient.Transport.(*http.Transport)
	require.True(t, ok, "DefaultHTTPClient must carry the guarded transport")
	assert.Nil(t, tr.Proxy)
}
