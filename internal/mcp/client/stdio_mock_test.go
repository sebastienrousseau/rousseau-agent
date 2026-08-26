package client_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp/client"
)

// The tests in this file drive a real subprocess over the real stdio
// pipe, but the "MCP server" is a POSIX shell script rather than a
// compiled Go program. That keeps the tests hermetic *and* removes the
// dependency on a working Go toolchain (and a writable build cache) at
// test time, which the compile-a-fixture approach in client_test.go
// needs.
//
// The script answers line-delimited JSON-RPC. It extracts the id from
// the anchored envelope prefix (encoding/json emits Envelope fields in
// declaration order: jsonrpc, id, method, params) and dispatches on
// method plus, for tools/call, the params.name field. Test arguments
// must therefore not embed the literal substring `"method":"`.
const shellMockServer = `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9][0-9]*\).*/\1/p')
  method=$(printf '%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  name=$(printf '%s' "$line" | sed -n 's/.*"params":{"name":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"shellmock","version":"1.0"},"capabilities":{"tools":{}}}}\n' "$id"
      ;;
    notifications/initialized)
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"greet","description":"greets you","inputSchema":{"type":"object"}},{"name":"boom","description":"","inputSchema":{"type":"object"}}]}}\n' "$id"
      ;;
    tools/call)
      case "$name" in
        greet)
          printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"hello"},{"type":"image"}]}}\n' "$id"
          ;;
        boom)
          printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"tool failed"}],"isError":true}}\n' "$id"
          ;;
        rpcerr)
          printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"nope"}}\n' "$id"
          ;;
        badresult)
          printf '{"jsonrpc":"2.0","id":%s,"result":"not-an-object"}\n' "$id"
          ;;
        noisy)
          printf 'this line is not json at all\n'
          printf '{"jsonrpc":"2.0","method":"notifications/progress"}\n'
          printf '{"jsonrpc":"2.0","id":"a-string-id","result":{}}\n'
          printf '{"jsonrpc":"2.0","id":987654,"result":{}}\n'
          printf 'complaining on stderr\n' >&2
          printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"noisy ok"}]}}\n' "$id"
          ;;
        *)
          ;;
      esac
      ;;
  esac
done
`

// lingeringMockServer answers the handshake then refuses to exit when
// stdin closes, forcing Close down its kill-after-grace-period path.
const lingeringMockServer = shellMockServer + `
sleep 5
`

// newShellClient starts the shell mock and returns a connected client.
func newShellClient(t *testing.T, script string, mutate func(*client.Config)) *client.Client {
	t.Helper()
	cfg := client.Config{
		Name:           "shellmock",
		Command:        "/bin/sh",
		Args:           []string{"-c", script},
		StartTimeout:   10 * time.Second,
		RequestTimeout: 10 * time.Second,
		Logger:         discardLogger(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	cl, err := client.New(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() }) //nolint:errcheck // best-effort cleanup
	return cl
}

func TestClient_ShellMock_ListToolsAndCall(t *testing.T) {
	cl := newShellClient(t, shellMockServer, nil)
	assert.Equal(t, "shellmock", cl.Name())

	tools, err := cl.ListTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 2)
	assert.Equal(t, "greet", tools[0].Name)
	assert.Equal(t, "greets you", tools[0].Description)

	res, err := cl.CallTool(context.Background(), "greet", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Len(t, res.Content, 2)
	assert.Equal(t, "hello", res.Content[0].Text)
	assert.False(t, res.IsError)
}

func TestClient_ShellMock_ServerReportedToolError(t *testing.T) {
	cl := newShellClient(t, shellMockServer, nil)
	res, err := cl.CallTool(context.Background(), "boom", nil)
	require.NoError(t, err, "isError results are not transport errors")
	assert.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "tool failed", res.Content[0].Text)
}

func TestClient_ShellMock_JSONRPCErrorSurfaces(t *testing.T) {
	cl := newShellClient(t, shellMockServer, nil)
	_, err := cl.CallTool(context.Background(), "rpcerr", nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "server returned error on tools/call")
	assert.ErrorContains(t, err, "-32000")
	assert.ErrorContains(t, err, "nope")
}

func TestClient_ShellMock_UndecodableResultSurfaces(t *testing.T) {
	cl := newShellClient(t, shellMockServer, nil)
	_, err := cl.CallTool(context.Background(), "badresult", nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "decode tools/call result")
}

// TestClient_ShellMock_TolerationOfNoise proves the read loop survives
// garbage lines, ID-less notifications, non-integer IDs and orphaned
// responses, still delivering the response the caller waits on.
func TestClient_ShellMock_TolerationOfNoise(t *testing.T) {
	cl := newShellClient(t, shellMockServer, nil)
	res, err := cl.CallTool(context.Background(), "noisy", nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "noisy ok", res.Content[0].Text)
}

func TestClient_ShellMock_RequestTimeout(t *testing.T) {
	cl := newShellClient(t, shellMockServer, func(c *client.Config) {
		c.RequestTimeout = 150 * time.Millisecond
	})
	_, err := cl.CallTool(context.Background(), "silent", nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "timed out")
}

func TestClient_ShellMock_ContextCancelBeatsTimeout(t *testing.T) {
	cl := newShellClient(t, shellMockServer, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := cl.CallTool(ctx, "silent", nil)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestClient_ShellMock_ShorterContextDeadlineWins covers the branch
// that clamps the per-request timeout to the caller's deadline.
func TestClient_ShellMock_ShorterContextDeadlineWins(t *testing.T) {
	cl := newShellClient(t, shellMockServer, func(c *client.Config) {
		c.RequestTimeout = time.Hour
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := cl.CallTool(ctx, "silent", nil)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "must honour the ctx deadline, not the hour-long default")
}

func TestClient_ShellMock_CloseIsIdempotent(t *testing.T) {
	cl := newShellClient(t, shellMockServer, nil)
	require.NoError(t, cl.Close())
	require.NoError(t, cl.Close(), "Close must be a no-op the second time")
}

func TestClient_ShellMock_RequestsAfterCloseFail(t *testing.T) {
	cl := newShellClient(t, shellMockServer, nil)
	require.NoError(t, cl.Close())

	_, err := cl.ListTools(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "closed")

	_, err = cl.CallTool(context.Background(), "greet", nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "closed")
}

// TestClient_ShellMock_CloseKillsLingeringProcess exercises the
// grace-period-then-kill branch: the mock ignores stdin EOF and sleeps
// well past the 1s window, so Close must kill it and still return.
func TestClient_ShellMock_CloseKillsLingeringProcess(t *testing.T) {
	cl := newShellClient(t, lingeringMockServer, nil)
	start := time.Now()
	require.NoError(t, cl.Close())
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 900*time.Millisecond, "Close should wait out the grace period")
	assert.Less(t, elapsed, 4*time.Second, "Close must kill rather than wait for the 5s sleep")
}

func TestClient_ShellMock_EnvOverridesReachSubprocess(t *testing.T) {
	// The mock only completes the handshake when the injected variable
	// arrives intact, so a successful New proves the env plumbing.
	const script = `
read -r line
if [ "$ROUSSEAU_MCP_TEST" = "injected" ]; then
  printf '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"envmock","version":"1.0"},"capabilities":{}}}\n'
fi
cat > /dev/null
`
	cl := newShellClient(t, script, func(c *client.Config) {
		c.Env = map[string]string{"ROUSSEAU_MCP_TEST": "injected"}
	})
	assert.Equal(t, "shellmock", cl.Name())
}

func TestClient_New_StartFailureIsReported(t *testing.T) {
	_, err := client.New(context.Background(), client.Config{
		Name:    "missing",
		Command: "/nonexistent/rousseau-mcp-server",
		Logger:  discardLogger(),
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "start")
}

// TestClient_New_HandshakeFailureCleansUp uses a command that exits
// immediately, so the initialize request never gets an answer and New
// must tear the subprocess down and report the failure.
func TestClient_New_HandshakeFailureCleansUp(t *testing.T) {
	_, err := client.New(context.Background(), client.Config{
		Name:         "deadbeat",
		Command:      "/bin/sh",
		Args:         []string{"-c", "exit 0"},
		StartTimeout: 300 * time.Millisecond,
		Logger:       discardLogger(),
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "initialize")
}

func TestClient_CallTool_UnmarshalableArguments(t *testing.T) {
	cl := newShellClient(t, shellMockServer, nil)
	_, err := cl.CallTool(context.Background(), "greet", make(chan int))
	require.Error(t, err)
	assert.ErrorContains(t, err, "marshal arguments for greet")
}

func TestClient_New_DefaultsApplied(t *testing.T) {
	// Nil logger and zero timeouts must fall back to the documented
	// defaults rather than panicking or timing out instantly.
	cl, err := client.New(context.Background(), client.Config{
		Name:    "defaults",
		Command: "/bin/sh",
		Args:    []string{"-c", shellMockServer},
	})
	require.NoError(t, err)
	defer cl.Close() //nolint:errcheck // best-effort cleanup
	tools, err := cl.ListTools(context.Background())
	require.NoError(t, err)
	assert.Len(t, tools, 2)
}

// discardLogger silences the client's lifecycle logging in tests.
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
