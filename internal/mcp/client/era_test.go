package client_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp/client"
)

// modernServer speaks 2026-07-28 only: it answers server/discover,
// refuses initialize, and serves tools/list and tools/call only when
// the request carries the protocol version in _meta.
const modernServer = `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9][0-9]*\).*/\1/p')
  method=$(printf '%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$line" in
    *'"io.modelcontextprotocol/protocolVersion":"2026-07-28"'*) meta=yes ;;
    *) meta=no ;;
  esac
  case "$method" in
    server/discover)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}}}}\n' "$id"
      ;;
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"initialize was removed"}}\n' "$id"
      ;;
    tools/list)
      if [ "$meta" = yes ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","ttlMs":60000,"cacheScope":"global","tools":[{"name":"modern_ok","inputSchema":{"type":"object"}}]}}\n' "$id"
      else
        printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"missing _meta protocolVersion"}}\n' "$id"
      fi
      ;;
    tools/call)
      case "$line" in
        *'"name":"needs_input"'*)
          printf '{"jsonrpc":"2.0","id":%s,"result":{"resultType":"input_required","inputRequests":{},"requestState":"x"}}\n' "$id" ;;
        *)
          printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"meta=%s"}]}}\n' "$id" "$meta" ;;
      esac
      ;;
    *)
      [ -n "$id" ] && printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}\n' "$id"
      ;;
  esac
done
`

func TestClient_ModernServerSkipsInitializeAndSendsMeta(t *testing.T) {
	cl := newShellClient(t, modernServer, nil)
	assert.Equal(t, "2026-07-28", cl.ProtocolVersion())

	tools, err := cl.ListTools(context.Background())
	require.NoError(t, err, "tools/list carries _meta, so the modern server accepts it")
	require.Len(t, tools, 1)
	assert.Equal(t, "modern_ok", tools[0].Name)

	res, err := cl.CallTool(context.Background(), "echo", map[string]any{"x": 1})
	require.NoError(t, err)
	assert.Equal(t, "meta=yes", res.Content[0].Text, "tools/call carries _meta too; a missing resultType means complete")
}

func TestClient_ModernInputRequiredIsRefused(t *testing.T) {
	cl := newShellClient(t, modernServer, nil)
	_, err := cl.CallTool(context.Background(), "needs_input", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires client input")
}

// unsupportedServer is a 2026-07-28-era server that speaks some other
// revision; the client shares none with it and must not fall back.
const unsupportedServer = `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9][0-9]*\).*/\1/p')
  printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32022,"message":"unsupported protocol version","data":{"supported":["2027-01-01"],"requested":"2026-07-28"}}}\n' "$id"
done
`

func TestClient_UnsupportedProtocolVersionFailsWithoutFallback(t *testing.T) {
	_, err := client.New(context.Background(), client.Config{
		Name: "future", Command: "/bin/sh", Args: []string{"-c", unsupportedServer},
		StartTimeout: 5 * time.Second, Logger: discardLogger(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no protocol version in common")
	assert.Contains(t, err.Error(), "2027-01-01")
}

func TestClient_LegacyServerFallsBackToInitialize(t *testing.T) {
	cl := newShellClient(t, shellMockServer, nil)
	assert.Equal(t, "2024-11-05", cl.ProtocolVersion(), "the server's answer is the negotiated version")
	_, err := cl.ListTools(context.Background())
	require.NoError(t, err)
}

// silentServer ignores server/discover entirely, like a legacy server
// that drops unknown methods; the probe times out and initialize runs.
const silentServer = `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*'"protocolVersion":"2025-11-25"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-11-25","serverInfo":{"name":"quiet","version":"1"},"capabilities":{}}}\n' "$id" ;;
  esac
done
`

func TestClient_SilentServerFallsBackAfterProbeTimeout(t *testing.T) {
	start := time.Now()
	cl := newShellClient(t, silentServer, func(c *client.Config) { c.DiscoverTimeout = 200 * time.Millisecond })
	assert.Equal(t, "2025-11-25", cl.ProtocolVersion(), "initialize offers the latest legacy revision")
	assert.Less(t, time.Since(start), 5*time.Second)
}

// legacyListServer answers discover with only legacy revisions.
const legacyListServer = `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9][0-9]*\).*/\1/p')
  method=$(printf '%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    server/discover)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"supportedVersions":["2025-06-18"]}}\n' "$id" ;;
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"mid","version":"1"},"capabilities":{}}}\n' "$id" ;;
  esac
done
`

func TestClient_DiscoverListingOnlyLegacyVersionsUsesInitialize(t *testing.T) {
	cl := newShellClient(t, legacyListServer, nil)
	assert.Equal(t, "2025-06-18", cl.ProtocolVersion())
}

// oddVersionServer negotiates a revision the client does not know.
const oddVersionServer = `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9][0-9]*\).*/\1/p')
  method=$(printf '%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2023-01-01","serverInfo":{"name":"old","version":"1"},"capabilities":{}}}\n' "$id" ;;
    *)
      [ -n "$id" ] && printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}\n' "$id" ;;
  esac
done
`

func TestClient_UnknownNegotiatedVersionIsRejected(t *testing.T) {
	_, err := client.New(context.Background(), client.Config{
		Name: "old", Command: "/bin/sh", Args: []string{"-c", oddVersionServer},
		StartTimeout: 5 * time.Second, Logger: discardLogger(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `server chose "2023-01-01"`)
}
