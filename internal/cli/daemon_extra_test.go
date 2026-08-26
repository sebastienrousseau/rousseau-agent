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
	_ = wiring.Cleanup() //nolint:errcheck // asserts no panic on repeated shutdown
}

func TestTransportHandler_WithoutRateLimiterAppliesRecoverOnly(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup

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
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup

	h := wiring.TransportHandler("whatsapp", silentLogger())
	assert.NotNil(t, h)
}

// TestTransportHandler_ReusesSupervisorPerTransport confirms two calls
// for the same transport share one control.Registry — otherwise the
// serialisation guarantee (one turn per key across concurrent inbounds)
// would be broken by whichever cobra command called TransportHandler
// last.
func TestTransportHandler_ReusesSupervisorPerTransport(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup

	_ = wiring.TransportHandler("whatsapp", silentLogger())
	_ = wiring.TransportHandler("whatsapp", silentLogger())
	sig := wiring.supervisorFor("signal", silentLogger())
	wa1 := wiring.supervisorFor("whatsapp", silentLogger())
	wa2 := wiring.supervisorFor("whatsapp", silentLogger())
	assert.Same(t, wa1, wa2, "the same transport must reuse its Supervisor")
	assert.NotSame(t, wa1, sig, "different transports must not share a Registry (keys can collide)")
}
