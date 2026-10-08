package client_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp/client"
)

// oversizeLineServer completes the handshake, answers tools/call with
// a single 2 MiB line (over the client's 1 MiB line limit) and then
// stays alive, reading stdin, as a misbehaving server would.
const oversizeLineServer = `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9][0-9]*\).*/\1/p')
  method=$(printf '%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"big","version":"1"},"capabilities":{"tools":{}}}}\n' "$id"
      ;;
    tools/call)
      head -c 2097152 /dev/zero | tr '\0' x
      printf '\n'
      ;;
    *)
      [ -n "$id" ] && printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}\n' "$id"
      ;;
  esac
done
`

// TestClient_OversizedLineFailsFast proves a line over the scanner
// limit breaks the connection at once: the waiting request and every
// later one fail well inside RequestTimeout, naming the line limit,
// instead of each waiting out the full timeout.
func TestClient_OversizedLineFailsFast(t *testing.T) {
	cl := newShellClient(t, oversizeLineServer, func(c *client.Config) {
		c.RequestTimeout = 10 * time.Second
	})

	start := time.Now()
	_, err := cl.CallTool(context.Background(), "big", nil)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second, "the in-flight request must not wait out RequestTimeout")
	assert.ErrorContains(t, err, "1 MiB")

	start = time.Now()
	_, err = cl.CallTool(context.Background(), "big", nil)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second, "later requests must fail fast")
	assert.ErrorContains(t, err, "1 MiB")
}

// TestClient_ServerExitFailsFast covers the EOF path: a server that
// exits after the handshake must not leave requests hanging.
func TestClient_ServerExitFailsFast(t *testing.T) {
	const script = `
read -r line
printf '{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}\n'
read -r line
printf '{"jsonrpc":"2.0","id":2,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"quitter","version":"1"},"capabilities":{}}}\n'
read -r line
exit 0
`
	cl := newShellClient(t, script, func(c *client.Config) {
		c.RequestTimeout = 10 * time.Second
	})
	// Give the server a moment to exit after the initialized notification.
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	_, err := cl.ListTools(context.Background())
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
	assert.ErrorContains(t, err, "closed")
}
