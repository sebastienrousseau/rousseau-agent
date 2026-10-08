package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp/client"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	mcpadapter "github.com/sebastienrousseau/rousseau-agent/internal/tools/mcp"
)

// hostileMCPServer advertises tools whose names, descriptions and
// schemas a server could use to poison the registry or bloat the
// prompt, and a tool whose error body is far larger than any model
// needs to read.
const hostileMCPServer = `
desc=$(printf '%3000s' '' | tr ' ' d)
big=$(printf '%70000s' '' | tr ' ' s)
long=$(printf '%65s' '' | tr ' ' n)
err=$(printf '%10000s' '' | tr ' ' e)
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9][0-9]*\).*/\1/p')
  method=$(printf '%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"hostile","version":"1.0"},"capabilities":{"tools":{}}}}\n' "$id"
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[' "$id"
      printf '{"name":"good.tool-1","description":"fine"},'
      printf '{"name":"bad\\nname","description":"newline"},'
      printf '{"name":"b:c","description":"colon"},'
      printf '{"name":"%s","description":"too long"},' "$long"
      printf '{"name":"","description":"empty"},'
      printf '{"name":"longdesc","description":"%s"},' "$desc"
      printf '{"name":"bigschema","description":"x","inputSchema":{"type":"object","description":"%s"}},' "$big"
      printf '{"name":"hugeerr","description":"x"}'
      printf ']}}\n'
      ;;
    tools/call)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"%s"}],"isError":true}}\n' "$id" "$err"
      ;;
    *)
      [ -n "$id" ] && printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}\n' "$id"
      ;;
  esac
done
`

// registerHostile starts the hostile server and registers its tools.
func registerHostile(t *testing.T) (*tools.Registry, []string) {
	t.Helper()
	cl, err := client.New(context.Background(), client.Config{
		Name:           "hostile",
		Command:        "/bin/sh",
		Args:           []string{"-c", hostileMCPServer},
		StartTimeout:   10 * time.Second,
		RequestTimeout: 10 * time.Second,
		Logger:         discardLogger(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() }) //nolint:errcheck // best-effort cleanup
	registry := tools.NewRegistry()
	names, err := mcpadapter.RegisterClient(context.Background(), registry, cl)
	require.NoError(t, err, "skipped tools are not registration errors")
	return registry, names
}

func TestRegisterClient_SkipsInvalidToolNames(t *testing.T) {
	registry, names := registerHostile(t)
	assert.Equal(t, []string{
		"mcp:hostile:good.tool-1",
		"mcp:hostile:longdesc",
		"mcp:hostile:bigschema",
		"mcp:hostile:hugeerr",
	}, names)
	for _, bad := range []string{"bad\nname", "b:c", strings.Repeat("n", 65), ""} {
		_, ok := registry.Get("mcp:hostile:" + bad)
		assert.False(t, ok, "tool %q must not be registered", bad)
	}
}

func TestRegisterClient_TruncatesLongDescription(t *testing.T) {
	registry, _ := registerHostile(t)
	tool, ok := registry.Get("mcp:hostile:longdesc")
	require.True(t, ok)
	got := tool.Description()
	assert.True(t, strings.HasSuffix(got, "…"), "a truncated description is marked")
	assert.LessOrEqual(t, len(got), 2048+len(`[via MCP server "hostile"] `))
}

func TestRegisterClient_OversizedSchemaFallsBack(t *testing.T) {
	registry, _ := registerHostile(t)
	tool, ok := registry.Get("mcp:hostile:bigschema")
	require.True(t, ok)
	schema := tool.InputSchema()
	assert.Equal(t, true, schema["additionalProperties"], "an oversized schema is replaced by the permissive shape")
	assert.NotContains(t, schema, "description")
}

func TestAdapter_ExecuteTruncatesHugeErrorBody(t *testing.T) {
	registry, _ := registerHostile(t)
	tool, ok := registry.Get("mcp:hostile:hugeerr")
	require.True(t, ok)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
	msg := err.Error()
	assert.LessOrEqual(t, len(msg), 4096+len("mcp mcp:hostile:hugeerr: "))
	assert.True(t, strings.HasSuffix(msg, "…"), "a truncated error body is marked")
}
