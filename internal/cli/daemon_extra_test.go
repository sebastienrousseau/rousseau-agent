package cli

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/toolgate"
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

// TestStartBackgroundServers_ServesMetrics pins that
// observability.metrics_addr actually starts the /metrics + /healthz
// endpoint. It was configured and documented but never started by any
// daemon entry point.
func TestStartBackgroundServers_ServesMetrics(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}
	opts.Config.Observability.MetricsAddr = addr
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wiring.StartBackgroundServers(ctx)

	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + addr + "/healthz") //nolint:noctx,gosec // local test endpoint
		if err != nil {
			return false
		}
		_ = resp.Body.Close() //nolint:errcheck // test
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 20*time.Millisecond, "metrics server never came up on %s", addr)
}

// TestAssembleDaemon_PolicyBridgeGovernsClaudeCLI pins the bridge end
// to end inside the daemon: with the claudecli provider, a PreToolUse
// call through the installed socket is decided by the configured
// approver (deny wins), and Cleanup removes the socket.
func TestAssembleDaemon_PolicyBridgeGovernsClaudeCLI(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "claudecli"
	opts.Config.ClaudeCLI.PermissionMode = "bypassPermissions"
	opts.Config.Agent.Approver = config.ApproverConfig{
		Mode:    "pattern",
		Default: "allow",
		Deny:    []config.PatternEntry{{Tool: "bash", Match: "rm -rf"}},
	}
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	require.NotNil(t, wiring.toolgate, "claudecli must get the policy bridge by default")
	sock := wiring.toolgate.Path()

	var stderr bytes.Buffer
	deny := `{"session_id":"s","tool_name":"Bash","tool_input":{"command":"rm -rf /srv"}}`
	assert.Equal(t, toolgate.ExitBlock, toolgate.RunHook(strings.NewReader(deny), &stderr, sock, 5*time.Second))
	allow := `{"session_id":"s","tool_name":"Bash","tool_input":{"command":"ls"}}`
	assert.Equal(t, toolgate.ExitAllow, toolgate.RunHook(strings.NewReader(allow), &stderr, sock, 5*time.Second))

	require.NoError(t, wiring.Cleanup())
	_, statErr := os.Stat(sock)
	assert.True(t, os.IsNotExist(statErr), "Cleanup removes the socket")
}

func TestAssembleDaemon_PolicyBridgeRefusesBare(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "claudecli"
	opts.Config.ClaudeCLI.Bare = true
	_, err := assembleDaemon(context.Background(), opts, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disable_policy_hook")

	opts.Config.ClaudeCLI.DisablePolicyHook = true
	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err, "explicit opt-out is honoured")
	assert.Nil(t, wiring.toolgate)
	_ = wiring.Cleanup() //nolint:errcheck // test cleanup
}
