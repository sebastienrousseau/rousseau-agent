package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
)

func TestA2ADefaultListenIsLoopback(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "127.0.0.1:8443", defaultA2AListen)
}

// TestCheckListenTLS is the bind/TLS validation table shared by the
// A2A and SCIM servers.
func TestCheckListenTLS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		addr      string
		cert, key string
		plaintext bool
		wantErr   bool
	}{
		{"ipv4 loopback", "127.0.0.1:8443", "", "", false, false},
		{"ipv6 loopback", "[::1]:8443", "", "", false, false},
		{"localhost", "localhost:8443", "", "", false, false},
		{"all interfaces", ":8443", "", "", false, true},
		{"unspecified v4", "0.0.0.0:8443", "", "", false, true},
		{"unspecified v6", "[::]:8443", "", "", false, true},
		{"lan address", "192.168.1.10:8443", "", "", false, true},
		{"hostname", "agent.internal:8443", "", "", false, true},
		{"all interfaces with tls", ":8443", "c.pem", "k.pem", false, false},
		{"all interfaces allow_plaintext", ":8443", "", "", true, false},
		{"cert without key", "127.0.0.1:8443", "c.pem", "", false, true},
		{"key without cert", ":8443", "", "k.pem", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkListenTLS(tc.addr, tc.cert, tc.key, tc.plaintext)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBuildA2AServer_NonLoopbackPlaintextRefused(t *testing.T) {
	t.Parallel()
	base := config.A2AServerConfig{Enabled: true, AuthTokensFile: writeTokenFile(t), Listen: "0.0.0.0:8443"}
	assert.Nil(t, buildA2AServer(config.A2AConfig{Server: base}, fakeAgent{}.agent(), a2aSilentLogger()),
		"non-loopback bind without TLS must refuse to start")

	opted := base
	opted.AllowPlaintext = true
	assert.NotNil(t, buildA2AServer(config.A2AConfig{Server: opted}, fakeAgent{}.agent(), a2aSilentLogger()))

	withTLS := base
	withTLS.TLSCertFile, withTLS.TLSKeyFile = "c.pem", "k.pem"
	rt := buildA2AServer(config.A2AConfig{Server: withTLS}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt)
	assert.Equal(t, "c.pem", rt.TLSCertFile)
	assert.Equal(t, "k.pem", rt.TLSKeyFile)
}

func TestA2AListenRow(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "info", a2aListenRow(config.A2AServerConfig{}).Status)
	row := a2aListenRow(config.A2AServerConfig{Listen: ":8443"})
	assert.Equal(t, "warn", row.Status)
	assert.Contains(t, row.Detail, "will not start")
}

func TestBuildA2AServer_InflightLimitsWired(t *testing.T) {
	t.Parallel()
	rt := buildA2AServer(config.A2AConfig{Server: config.A2AServerConfig{
		Enabled: true, AuthTokensFile: writeTokenFile(t),
		MaxInflightPerPeer: 2, MaxInflight: 9,
	}}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt)
	assert.Equal(t, 2, rt.Server.MaxInflightPerPeer)
	assert.Equal(t, 9, rt.Server.MaxInflight)
}

type ssoOnChecker struct{}

func (ssoOnChecker) IsEnabled(license.Feature) bool { return true }
func (ssoOnChecker) Tier() license.Tier             { return license.TierEnterprise }
func (ssoOnChecker) Info() license.Info             { return license.Info{} }

func TestBuildSCIM_NonLoopbackPlaintextRefused(t *testing.T) {
	t.Parallel()
	srv, addr, err := buildSCIM(context.Background(), config.SCIMConfig{
		Addr: "0.0.0.0:7643", BearerToken: "test-scim-token",
	}, ssoOnChecker{}, nil, a2aSilentLogger())
	require.NoError(t, err)
	assert.Nil(t, srv, "SCIM must not bind a non-loopback plaintext address")
	assert.Empty(t, addr)
}

// writeTestCert writes a self-signed ECDSA certificate for 127.0.0.1
// and returns the cert path, key path and a pool trusting it.
func writeTestCert(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "a2a-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return certPath, keyPath, pool
}

// TestRunA2AServer_TLSRoundTrip: with tls_cert_file / tls_key_file the
// A2A endpoint speaks HTTPS.
func TestRunA2AServer_TLSRoundTrip(t *testing.T) {
	t.Parallel()
	certPath, keyPath, pool := writeTestCert(t)
	rt := buildA2AServer(config.A2AConfig{Server: config.A2AServerConfig{
		Enabled: true, AuthTokensFile: writeTokenFile(t),
		TLSCertFile: certPath, TLSKeyFile: keyPath,
	}}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	rt.Addr = ln.Addr().String()
	require.NoError(t, ln.Close())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runA2AServer(ctx, rt, a2aSilentLogger())

	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
	url := "https://" + rt.Addr + "/.well-known/agent-card.json"
	require.Eventually(t, func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		res, err := client.Do(req)
		if err != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == http.StatusOK && res.TLS != nil
	}, 3*time.Second, 50*time.Millisecond, "HTTPS GET of the agent card must succeed")
}
