package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

// TestSpec_GetAgentCard_LegacyFallbackRejectedWhenKeysTrusted: once
// the operator pins publisher keys, a peer (or an attacker in the
// path) answering 404 on the v1 card must not downgrade the client to
// the unsignable legacy card, even without RequireSignedCard.
func TestSpec_GetAgentCard_LegacyFallbackRejectedWhenKeysTrusted(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/agent-card.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(a2a.CapabilityCard{Name: "downgraded", Version: "v0"}) //nolint:errcheck // test
	}))
	t.Cleanup(ts.Close)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	c, err := New(Config{
		Name: "peer", Endpoint: ts.URL, Timeout: 5 * time.Second,
		TrustedPublisherKeys: []ed25519.PublicKey{pub},
	})
	require.NoError(t, err)
	card, err := c.GetAgentCard(context.Background())
	require.Error(t, err, "legacy fallback must be refused when trusted keys are configured; got card %q", card.Name)
	assert.Contains(t, err.Error(), "legacy cards cannot be signed")
}
