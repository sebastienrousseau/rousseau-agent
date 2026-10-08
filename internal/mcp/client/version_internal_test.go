package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp"
)

func TestCheckResultType(t *testing.T) {
	assert.NoError(t, checkResultType(nil))
	assert.NoError(t, checkResultType(json.RawMessage(`{}`)), "missing resultType means complete")
	assert.NoError(t, checkResultType(json.RawMessage(`{"resultType":"complete"}`)))
	assert.NoError(t, checkResultType(json.RawMessage(`"not-an-object"`)), "shape errors are the decoder's job")
	assert.ErrorContains(t, checkResultType(json.RawMessage(`{"resultType":"input_required"}`)), "client input")
	assert.ErrorContains(t, checkResultType(json.RawMessage(`{"resultType":"partial"}`)), `invalid resultType "partial"`)
}

func TestWithMeta(t *testing.T) {
	meta := map[string]any{"k": "v"}

	raw, err := withMeta(nil, meta)
	require.NoError(t, err)
	assert.JSONEq(t, `{"_meta":{"k":"v"}}`, string(raw))

	raw, err = withMeta(mcp.ToolsCallParams{Name: "t", Arguments: json.RawMessage(`{"a":1}`)}, meta)
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"t","arguments":{"a":1},"_meta":{"k":"v"}}`, string(raw))

	raw, err = withMeta(json.RawMessage(`{"_meta":{"progressToken":7}}`), meta)
	require.NoError(t, err)
	assert.JSONEq(t, `{"_meta":{"progressToken":7,"k":"v"}}`, string(raw), "existing _meta keys are kept")

	_, err = withMeta(json.RawMessage(`[1]`), meta)
	assert.ErrorContains(t, err, "JSON object")
	_, err = withMeta(json.RawMessage(`{"_meta":3}`), meta)
	assert.ErrorContains(t, err, "_meta must be")
	_, err = withMeta(make(chan int), meta)
	assert.Error(t, err)
}

// An abandoned request tells the server, so it can stop the work.
func TestRequest_TimeoutSendsCancelled(t *testing.T) {
	w := &nopWriteCloser{}
	c := newTestClient(w)
	c.timeout = 20 * time.Millisecond
	err := c.request(context.Background(), mcp.MethodToolsCall, nil, nil)
	require.Error(t, err)

	lines := strings.Split(strings.TrimSpace(w.buf.String()), "\n")
	require.Len(t, lines, 2)
	var env mcp.Envelope
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &env))
	assert.Equal(t, mcp.MethodCancelled, env.Method)
	assert.Empty(t, env.ID, "a notification carries no id")
	assert.JSONEq(t, `{"requestId":1,"reason":"timed out"}`, string(env.Params))
}

func TestRequest_ContextCancelSendsCancelled(t *testing.T) {
	w := &nopWriteCloser{}
	c := newTestClient(w)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.request(ctx, mcp.MethodToolsCall, nil, nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, w.buf.String(), `"reason":"cancelled by client"`)
}

// In modern mode the request params carry the _meta keys.
func TestRequest_ModernAddsMeta(t *testing.T) {
	w := &nopWriteCloser{}
	c := newTestClient(w)
	c.modern = true
	c.version = "1.2.3"
	c.timeout = 10 * time.Millisecond
	_ = c.request(context.Background(), mcp.MethodToolsList, nil, nil) //nolint:errcheck // only the written frame matters
	first := strings.SplitN(w.buf.String(), "\n", 2)[0]
	assert.Contains(t, first, `"io.modelcontextprotocol/protocolVersion":"2026-07-28"`)
	assert.Contains(t, first, `"io.modelcontextprotocol/clientCapabilities":{}`)
	assert.Contains(t, first, `"version":"1.2.3"`)
}

func TestRPCErrorMessage(t *testing.T) {
	e := &RPCError{Server: "s", Method: "m", Code: -1, Message: "boom"}
	assert.Equal(t, "mcp/client s: server returned error on m: [-1] boom", e.Error())
}

func TestCheckResultType_MalformedObject(t *testing.T) {
	assert.ErrorContains(t, checkResultType(json.RawMessage(`{"resultType":3}`)), "decode resultType")
}
