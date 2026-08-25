package mcp_test

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
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	mcpadapter "github.com/sebastienrousseau/rousseau-agent/internal/tools/mcp"
)

// The adapter is exercised against a real MCP server subprocess, but
// the server is a POSIX shell script speaking line-delimited JSON-RPC
// rather than a compiled Go fixture. That keeps the test hermetic and
// independent of a working Go toolchain at test time.
//
// The script reads the id from the anchored envelope prefix
// (encoding/json emits Envelope fields in declaration order) and
// dispatches on method, plus params.name for tools/call.
const shellMCPServer = `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9][0-9]*\).*/\1/p')
  method=$(printf '%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  name=$(printf '%s' "$line" | sed -n 's/.*"params":{"name":"\([^"]*\)".*/\1/p')
  args=$(printf '%s' "$line" | sed -n 's/.*"arguments":\(.*\)}}$/\1/p' | tr -d '"')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"shellmock","version":"1.0"},"capabilities":{"tools":{}}}}\n' "$id"
      ;;
    notifications/initialized)
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"greet","description":"greets you","inputSchema":{"type":"object","properties":{"who":{"type":"string"}}}},{"name":"boom","description":""}]}}\n' "$id"
      ;;
    tools/call)
      case "$name" in
        greet)
          printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"args=%s"},{"type":"image"}]}}\n' "$id" "$args"
          ;;
        boom)
          printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"the tool blew up"}],"isError":true}}\n' "$id"
          ;;
        *)
          printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"unknown tool"}}\n' "$id"
          ;;
      esac
      ;;
  esac
done
`

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newShellMCPClient starts the shell MCP server and returns a client.
func newShellMCPClient(t *testing.T) *client.Client {
	t.Helper()
	cl, err := client.New(context.Background(), client.Config{
		Name:           "shellmock",
		Command:        "/bin/sh",
		Args:           []string{"-c", shellMCPServer},
		StartTimeout:   10 * time.Second,
		RequestTimeout: 10 * time.Second,
		Logger:         discardLogger(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() }) //nolint:errcheck // best-effort cleanup
	return cl
}

func TestRegisterClient_ShellMock_RegistersAndForwards(t *testing.T) {
	cl := newShellMCPClient(t)
	registry := tools.NewRegistry()

	names, err := mcpadapter.RegisterClient(context.Background(), registry, cl)
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp:shellmock:greet", "mcp:shellmock:boom"}, names)

	tool, ok := registry.Get("mcp:shellmock:greet")
	require.True(t, ok)
	assert.Contains(t, tool.Description(), `[via MCP server "shellmock"]`)
	assert.Equal(t, "object", tool.InputSchema()["type"])

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"who":"seb"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "args={who:seb}")
	assert.Contains(t, out, "[image content]", "non-text blocks get a placeholder")
}

// TestAdapter_ExecuteEmptyInputBecomesEmptyObject pins the behaviour
// that MCP servers requiring an arguments object still get one when
// the model calls a no-argument tool.
func TestAdapter_ExecuteEmptyInputBecomesEmptyObject(t *testing.T) {
	cl := newShellMCPClient(t)
	registry := tools.NewRegistry()
	_, err := mcpadapter.RegisterClient(context.Background(), registry, cl)
	require.NoError(t, err)

	tool, ok := registry.Get("mcp:shellmock:greet")
	require.True(t, ok)
	out, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Contains(t, out, "args={}")
}

// TestAdapter_ExecuteServerReportedError checks that isError=true
// becomes a Go error while still returning the body, so the agent can
// show the model what went wrong.
func TestAdapter_ExecuteServerReportedError(t *testing.T) {
	cl := newShellMCPClient(t)
	registry := tools.NewRegistry()
	_, err := mcpadapter.RegisterClient(context.Background(), registry, cl)
	require.NoError(t, err)

	tool, ok := registry.Get("mcp:shellmock:boom")
	require.True(t, ok)
	assert.Contains(t, tool.Description(), "no description")
	assert.Equal(t, true, tool.InputSchema()["additionalProperties"],
		"an empty server schema falls back to the permissive shape")

	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
	assert.ErrorContains(t, err, "mcp mcp:shellmock:boom: server reported error")
	assert.Equal(t, "the tool blew up", out, "the body is still returned for the model")
}

func TestAdapter_ExecuteTransportFailure(t *testing.T) {
	cl := newShellMCPClient(t)
	registry := tools.NewRegistry()
	_, err := mcpadapter.RegisterClient(context.Background(), registry, cl)
	require.NoError(t, err)

	tool, ok := registry.Get("mcp:shellmock:greet")
	require.True(t, ok)
	require.NoError(t, cl.Close()) // the server is gone

	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
	assert.ErrorContains(t, err, "mcp mcp:shellmock:greet")
	assert.Empty(t, out)
}

// TestRegisterClient_DuplicateNamesAggregate proves one failing
// registration does not abort the others and that every failure is
// reported.
func TestRegisterClient_DuplicateNamesAggregate(t *testing.T) {
	cl := newShellMCPClient(t)
	registry := tools.NewRegistry()

	first, err := mcpadapter.RegisterClient(context.Background(), registry, cl)
	require.NoError(t, err)
	require.Len(t, first, 2)

	second, err := mcpadapter.RegisterClient(context.Background(), registry, cl)
	require.Error(t, err)
	assert.Empty(t, second, "nothing new could be registered")
	assert.ErrorContains(t, err, "register mcp:shellmock:greet")
	assert.ErrorContains(t, err, "register mcp:shellmock:boom")
}

func TestRegisterClient_ListToolsFailure(t *testing.T) {
	cl := newShellMCPClient(t)
	require.NoError(t, cl.Close())

	_, err := mcpadapter.RegisterClient(context.Background(), tools.NewRegistry(), cl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "list tools for shellmock")
}
