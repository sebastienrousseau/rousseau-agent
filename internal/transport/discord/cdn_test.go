package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// trustTestCDN points c's attachment policy at an httptest server.
func trustTestCDN(t *testing.T, c *Client, srvURL string) {
	t.Helper()
	u, err := url.Parse(srvURL)
	require.NoError(t, err)
	c.cdn = cdnPolicy{pattern: transport.FetchPattern(u.Scheme, u.Host)}
}

func TestCDNPolicy_Allowed(t *testing.T) {
	p := defaultCDNPolicy()
	for raw, want := range map[string]bool{
		"https://cdn.discordapp.com/attachments/1/2/a.ogg":   true,
		"https://media.discordapp.net/attachments/1/2/a.png": true,
		"http://cdn.discordapp.com/a.ogg":                    false,
		"https://cdn.discordapp.com:8443/a.ogg":              false,
		"https://cdn.discordapp.com.evil.test/a.ogg":         false,
		"https://user@cdn.discordapp.com/a.ogg":              false,
		"https://169.254.169.254/latest/meta-data":           false,
		"https://exa\x7fmple.invalid/a.ogg":                  false,
		"":                                                   false,
	} {
		assert.Equal(t, want, p.pattern.MatchString(raw), raw)
	}
}

func TestDownloadAttachment_RefusesNonCDNHost(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()

	c, err := New(Config{Token: "t", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	_, err = c.downloadAttachment(context.Background(), srv.URL+"/a.ogg")
	require.ErrorContains(t, err, "not a Discord CDN URL")
	assert.False(t, hit, "no request may reach a non-CDN host")
}
