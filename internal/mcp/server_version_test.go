package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func echoServer() *Server {
	s := NewServer("rousseau-test", "9.9.9", silentLogger())
	s.MustRegister(ToolSpec{
		Name:        "echo",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, args json.RawMessage) ([]Content, error) {
			return TextContent(string(args)), nil
		},
	})
	return s
}

const modernMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"t","version":"1"}}`

func resultObject(t *testing.T, env Envelope) map[string]any {
	t.Helper()
	require.Nil(t, env.Error, "unexpected error: %+v", env.Error)
	var m map[string]any
	require.NoError(t, json.Unmarshal(env.Result, &m))
	return m
}

func TestServer_InitializeEchoesSupportedVersion(t *testing.T) {
	for _, v := range LegacyProtocolVersions {
		resp := call(t, echoServer(), MethodInitialize, json.RawMessage(`1`), json.RawMessage(`{"protocolVersion":"`+v+`","clientInfo":{"name":"c","version":"1"}}`))
		var r InitializeResult
		require.NoError(t, json.Unmarshal(resp.Result, &r))
		assert.Equal(t, v, r.ProtocolVersion, "a supported version is echoed")
	}
}

func TestServer_InitializeOffersLatestLegacyForUnknownVersion(t *testing.T) {
	for _, params := range []string{`{"protocolVersion":"2099-01-01"}`, `{"protocolVersion":"2026-07-28"}`, ``} {
		var raw json.RawMessage
		if params != "" {
			raw = json.RawMessage(params)
		}
		resp := call(t, echoServer(), MethodInitialize, json.RawMessage(`1`), raw)
		var r InitializeResult
		require.NoError(t, json.Unmarshal(resp.Result, &r))
		assert.Equal(t, LatestLegacyProtocolVersion, r.ProtocolVersion, "params %q", params)
	}
}

func TestServer_Discover(t *testing.T) {
	m := resultObject(t, call(t, echoServer(), MethodDiscover, json.RawMessage(`1`), json.RawMessage(`{`+modernMeta+`}`)))
	assert.Equal(t, "complete", m["resultType"])
	assert.Equal(t, []any{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}, m["supportedVersions"])
	assert.Equal(t, "public", m["cacheScope"])
	assert.InDelta(t, 300000, m["ttlMs"], 0)
	assert.Contains(t, m["capabilities"], "tools")
	info := m["_meta"].(map[string]any)["io.modelcontextprotocol/serverInfo"].(map[string]any)
	assert.Equal(t, "rousseau-test", info["name"])
	assert.Equal(t, "9.9.9", info["version"])
}

// A 2026-07-28 client needs no initialize: tools/list and tools/call
// work straight away and every result says resultType complete.
func TestServer_StatelessRequestsWithoutInitialize(t *testing.T) {
	s := echoServer()
	list := resultObject(t, call(t, s, MethodToolsList, json.RawMessage(`1`), json.RawMessage(`{`+modernMeta+`}`)))
	assert.Equal(t, "complete", list["resultType"])
	assert.Equal(t, "public", list["cacheScope"])
	assert.InDelta(t, 300000, list["ttlMs"], 0)
	require.Len(t, list["tools"], 1)

	callRes := resultObject(t, call(t, s, MethodToolsCall, json.RawMessage(`2`), json.RawMessage(`{"name":"echo","arguments":{"x":1},`+modernMeta+`}`)))
	assert.Equal(t, "complete", callRes["resultType"])
	_, hasTTL := callRes["ttlMs"]
	assert.False(t, hasTTL, "only tools/list and discover carry cache hints")
}

// Initialize-era results are unchanged: no resultType.
func TestServer_LegacyResultsHaveNoResultType(t *testing.T) {
	list := resultObject(t, call(t, echoServer(), MethodToolsList, json.RawMessage(`1`), nil))
	_, ok := list["resultType"]
	assert.False(t, ok)

	// A legacy request with a progressToken in _meta is still legacy.
	list = resultObject(t, call(t, echoServer(), MethodToolsList, json.RawMessage(`2`), json.RawMessage(`{"_meta":{"progressToken":3}}`)))
	_, ok = list["resultType"]
	assert.False(t, ok)
}

func TestServer_StatelessUnsupportedVersion(t *testing.T) {
	resp := call(t, echoServer(), MethodToolsList, json.RawMessage(`1`),
		json.RawMessage(`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}}`))
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeUnsupportedProtocolVersion, resp.Error.Code)
	data := resp.Error.Data.(map[string]any)
	assert.Equal(t, "2099-01-01", data["requested"])
	assert.Contains(t, data["supported"], "2026-07-28")

	// A legacy revision named statelessly is unsupported too: those
	// revisions need initialize.
	resp = call(t, echoServer(), MethodToolsList, json.RawMessage(`2`),
		json.RawMessage(`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-11-25","io.modelcontextprotocol/clientCapabilities":{}}}`))
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeUnsupportedProtocolVersion, resp.Error.Code)
}

func TestServer_StatelessMissingCapabilitiesIsInvalidParams(t *testing.T) {
	resp := call(t, echoServer(), MethodToolsList, json.RawMessage(`1`),
		json.RawMessage(`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`))
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeInvalidParams, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "clientCapabilities")
}

// Stateless errors (unknown tool) pass through without a resultType.
func TestServer_StatelessErrorPassesThrough(t *testing.T) {
	resp := call(t, echoServer(), MethodToolsCall, json.RawMessage(`1`), json.RawMessage(`{"name":"nope",`+modernMeta+`}`))
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeToolNotFound, resp.Error.Code)
}

// Notifications are never answered, including unknown ones and
// stateless ones with a bad version.
func TestServer_NotificationsAreNotAnswered(t *testing.T) {
	s := echoServer()
	in := bytes.NewBufferString(
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/whatever","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01"}}}` + "\n")
	out := &bytes.Buffer{}
	require.NoError(t, s.Serve(context.Background(), in, out))
	assert.Empty(t, out.String())
}

func TestMarkComplete_EdgeCases(t *testing.T) {
	assert.Nil(t, markComplete(nil, MethodToolsList))
	nonObject := &Envelope{JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Result: json.RawMessage(`[]`)}
	assert.Equal(t, `[]`, string(markComplete(nonObject, MethodToolsList).Result), "non-object results are left alone")
}

func TestRequestMeta(t *testing.T) {
	_, ok := requestMeta(nil)
	assert.False(t, ok)
	_, ok = requestMeta(json.RawMessage(`[1]`))
	assert.False(t, ok)
	_, ok = requestMeta(json.RawMessage(`{"_meta":{"progressToken":1}}`))
	assert.False(t, ok)
	_, ok = requestMeta(json.RawMessage(`{` + modernMeta + `}`))
	assert.True(t, ok)
}
