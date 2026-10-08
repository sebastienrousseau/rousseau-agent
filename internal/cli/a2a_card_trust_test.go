package cli

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestBuildA2AServer_SigningKeyWithoutPublicURLIsUnsigned(t *testing.T) {
	t.Parallel()
	keyPath, _, _ := writeSigningKey(t)
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{
			Enabled:        true,
			AuthTokensFile: writeTokenFile(t),
			SigningKeyFile: keyPath,
		},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt)
	assert.False(t, rt.Signed, "without public_url the card is served unsigned")
}

func TestBuildA2AServer_TrustedProxies(t *testing.T) {
	t.Parallel()
	rt := buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{
			Enabled:        true,
			AuthTokensFile: writeTokenFile(t),
			TrustedProxies: []string{"10.0.0.0/8", "192.0.2.7"},
		},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	require.NotNil(t, rt)
	assert.Equal(t, []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.0.2.7/32"),
	}, rt.Server.TrustedProxies)

	rt = buildA2AServer(config.A2AConfig{
		Server: config.A2AServerConfig{
			Enabled:        true,
			AuthTokensFile: writeTokenFile(t),
			TrustedProxies: []string{"not-an-ip"},
		},
	}, fakeAgent{}.agent(), a2aSilentLogger())
	assert.Nil(t, rt, "an unparsable trusted_proxies entry must refuse to start")
}
