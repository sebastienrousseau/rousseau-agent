package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// fetchCard serves one well-known card request through the router.
func fetchCard(t *testing.T, s *Server, remote string, hdr map[string]string) a2a.AgentCard {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
	req.Host = "internal.example:8443"
	if remote != "" {
		req.RemoteAddr = remote
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	s.Router().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var card a2a.AgentCard
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &card))
	return card
}

func signingServer(t *testing.T) (*Server, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s, err := New(a2a.CapabilityCard{Name: "n", Version: "v"}, echoHandler{}, nil)
	require.NoError(t, err)
	s.SigningKey = priv
	return s, pub
}

var forgedHost = map[string]string{
	"X-Forwarded-Proto": "https",
	"X-Forwarded-Host":  "evil.example",
}

// TestCard_ForwardedHostNotSigned: without a configured public URL the
// operator's key must never sign a card whose URL a caller chose.
func TestCard_ForwardedHostNotSigned(t *testing.T) {
	s, _ := signingServer(t)
	card := fetchCard(t, s, "", forgedHost)
	assert.Empty(t, card.Signatures, "a request-derived card must be served unsigned")
	assert.NotContains(t, card.URL, "evil.example", "forwarded headers from an untrusted client must be ignored")
}

// TestCard_PublicURLSigned: with a public URL the card is signed and
// advertises only that URL, whatever the request headers say.
func TestCard_PublicURLSigned(t *testing.T) {
	s, pub := signingServer(t)
	s.PublicURL = "https://agent.example.com"
	card := fetchCard(t, s, "", forgedHost)
	assert.Equal(t, "https://agent.example.com", card.URL)
	require.Len(t, card.Interfaces, 2)
	assert.Equal(t, "https://agent.example.com/jsonrpc", card.Interfaces[1].URL)
	require.NotEmpty(t, card.Signatures)
	require.NoError(t, a2a.VerifyAgentCard(card, []ed25519.PublicKey{pub}))
}

// TestCard_ForwardedHeadersOnlyFromTrustedProxy: forwarded headers
// shape the (unsigned) card only when the request came from a
// configured proxy network.
func TestCard_ForwardedHeadersOnlyFromTrustedProxy(t *testing.T) {
	s, err := New(a2a.CapabilityCard{Name: "n", Version: "v"}, echoHandler{}, nil)
	require.NoError(t, err)
	s.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}

	card := fetchCard(t, s, "10.1.2.3:5555", forgedHost)
	assert.Equal(t, "https://evil.example", card.URL, "trusted proxy headers are honoured")

	card = fetchCard(t, s, "203.0.113.9:5555", forgedHost)
	assert.Equal(t, "http://internal.example:8443", card.URL, "untrusted client headers are ignored")
}
