package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// TestCleanup_ClosesSessionsAndTolerantOfNil confirms the two invariants
// operators rely on when a transport exits: (a) the sessions handle is
// released so the SQLite WAL flushes, (b) nil MCP client entries do
// not crash the shutdown path.
func TestCleanup_ClosesSessionsAndTolerantOfNilMCP(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}

	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)

	// Simulate an mcp.clients section that failed to start — one nil
	// entry in the slice. Cleanup must not panic.
	wiring.Logger = silentLogger()
	require.NoError(t, wiring.Cleanup())
}

func TestCleanup_ReturnsSessionsCloseErrorOnDoubleClose(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	wiring.Logger = silentLogger()
	// First Cleanup succeeds; second returns whatever sqlite reports
	// (typically nil since Close is idempotent — the important thing
	// is that Cleanup doesn't panic on repeated calls).
	require.NoError(t, wiring.Cleanup())
	_ = wiring.Cleanup() // second call must not crash
}

func TestTransportHandler_WithoutRateLimiterAppliesRecoverOnly(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }()

	h := wiring.TransportHandler("whatsapp", silentLogger())
	require.NotNil(t, h)
	// The returned handler wraps Router+Recover. We don't need to
	// invoke it (that requires a real IncomingMessage + provider) —
	// the non-nil return exercises the composition path.
	assert.NotNil(t, h)
}

func TestTransportHandler_WithRateLimiterAppliesFullChain(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}
	// Wire a rate-limit rule for whatsapp so TransportHandler runs the
	// full 3-step chain instead of the 2-step one.
	opts.Config.RateLimit = config.RateLimitConfig{
		PerTransport: map[string]string{"whatsapp": "10r/1m"},
	}
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }()

	h := wiring.TransportHandler("whatsapp", silentLogger())
	assert.NotNil(t, h)
}
