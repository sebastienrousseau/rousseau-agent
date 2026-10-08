package client_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pagingServer serves tools/list in two pages: the first page returns
// nextCursor "p2", and a request carrying that cursor gets the rest.
const pagingServer = `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9][0-9]*\).*/\1/p')
  method=$(printf '%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"pager","version":"1"},"capabilities":{"tools":{}}}}\n' "$id"
      ;;
    tools/list)
      case "$line" in
        *'"cursor":"p2"'*)
          printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"c","inputSchema":{"type":"object"}}]}}\n' "$id"
          ;;
        *)
          printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"a","inputSchema":{"type":"object"}},{"name":"b","inputSchema":{"type":"object"}}],"nextCursor":"p2"}}\n' "$id"
          ;;
      esac
      ;;
    *)
      [ -n "$id" ] && printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}\n' "$id"
      ;;
  esac
done
`

// endlessServer always returns a next page.
const endlessServer = `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"jsonrpc":"2.0","id":\([0-9][0-9]*\).*/\1/p')
  method=$(printf '%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"loop","version":"1"},"capabilities":{}}}\n' "$id"
      ;;
    tools/list)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[],"nextCursor":"again"}}\n' "$id"
      ;;
    *)
      [ -n "$id" ] && printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}\n' "$id"
      ;;
  esac
done
`

func TestClient_ListToolsFollowsNextCursor(t *testing.T) {
	cl := newShellClient(t, pagingServer, nil)
	tools, err := cl.ListTools(context.Background())
	require.NoError(t, err)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	assert.Equal(t, []string{"a", "b", "c"}, names, "tools on the second page are not lost")
}

func TestClient_ListToolsBoundsEndlessPagination(t *testing.T) {
	cl := newShellClient(t, endlessServer, nil)
	_, err := cl.ListTools(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not finish within")
}
