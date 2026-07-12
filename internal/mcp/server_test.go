package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// call sends one JSON-RPC request through Serve and returns the
// decoded response envelope. Convenience for round-trip tests.
func call(t *testing.T, s *Server, method string, id, params json.RawMessage) Envelope {
	t.Helper()
	req := Envelope{JSONRPC: jsonRPCVersion, ID: id, Method: method, Params: params}
	b, err := json.Marshal(req)
	require.NoError(t, err)
	in := bytes.NewReader(append(b, '\n'))
	out := &bytes.Buffer{}
	require.NoError(t, s.Serve(context.Background(), in, out))
	var resp Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &resp))
	return resp
}

func TestServer_InitializeAndToolsList(t *testing.T) {
	s := NewServer("rousseau-test", "0.0.0", silentLogger())
	s.MustRegister(ToolSpec{
		Name:        "echo",
		Description: "echo back",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, args json.RawMessage) ([]Content, error) {
			return TextContent(string(args)), nil
		},
	})

	init := call(t, s, MethodInitialize, json.RawMessage(`1`), nil)
	assert.Nil(t, init.Error)

	list := call(t, s, MethodToolsList, json.RawMessage(`2`), nil)
	require.Nil(t, list.Error)
	var r ToolsListResult
	require.NoError(t, json.Unmarshal(list.Result, &r))
	require.Len(t, r.Tools, 1)
	assert.Equal(t, "echo", r.Tools[0].Name)
}

func TestServer_ToolsCall_HappyPath(t *testing.T) {
	s := NewServer("rousseau-test", "0.0.0", silentLogger())
	s.MustRegister(ToolSpec{
		Name: "greet",
		Handler: func(_ context.Context, args json.RawMessage) ([]Content, error) {
			var in struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, err
			}
			return TextContent("hello " + in.Name), nil
		},
	})
	params, _ := json.Marshal(ToolsCallParams{Name: "greet", Arguments: json.RawMessage(`{"name":"seb"}`)})
	resp := call(t, s, MethodToolsCall, json.RawMessage(`3`), params)
	require.Nil(t, resp.Error)
	var r ToolsCallResult
	require.NoError(t, json.Unmarshal(resp.Result, &r))
	require.Len(t, r.Content, 1)
	assert.Equal(t, "hello seb", r.Content[0].Text)
	assert.False(t, r.IsError)
}

func TestServer_ToolsCall_HandlerErrorReturnsIsError(t *testing.T) {
	s := NewServer("rousseau-test", "0.0.0", silentLogger())
	s.MustRegister(ToolSpec{
		Name: "bad",
		Handler: func(_ context.Context, _ json.RawMessage) ([]Content, error) {
			return nil, errors.New("boom")
		},
	})
	params, _ := json.Marshal(ToolsCallParams{Name: "bad"})
	resp := call(t, s, MethodToolsCall, json.RawMessage(`4`), params)
	require.Nil(t, resp.Error)
	var r ToolsCallResult
	require.NoError(t, json.Unmarshal(resp.Result, &r))
	assert.True(t, r.IsError)
	require.Len(t, r.Content, 1)
	assert.Equal(t, "boom", r.Content[0].Text)
}

func TestServer_UnknownTool(t *testing.T) {
	s := NewServer("rousseau-test", "0.0.0", silentLogger())
	params, _ := json.Marshal(ToolsCallParams{Name: "missing"})
	resp := call(t, s, MethodToolsCall, json.RawMessage(`5`), params)
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeToolNotFound, resp.Error.Code)
}

func TestServer_UnknownMethod(t *testing.T) {
	s := NewServer("rousseau-test", "0.0.0", silentLogger())
	resp := call(t, s, "bogus", json.RawMessage(`6`), nil)
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeMethodNotFound, resp.Error.Code)
}

func TestServer_MalformedJSON(t *testing.T) {
	s := NewServer("rousseau-test", "0.0.0", silentLogger())
	in := strings.NewReader(`{not json}` + "\n")
	out := &bytes.Buffer{}
	require.NoError(t, s.Serve(context.Background(), in, out))
	var resp Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeParseError, resp.Error.Code)
}

func TestServer_NotificationHasNoResponse(t *testing.T) {
	s := NewServer("rousseau-test", "0.0.0", silentLogger())
	req := Envelope{JSONRPC: jsonRPCVersion, Method: MethodInitialized}
	b, _ := json.Marshal(req)
	in := bytes.NewReader(append(b, '\n'))
	out := &bytes.Buffer{}
	require.NoError(t, s.Serve(context.Background(), in, out))
	assert.Empty(t, out.String())
}

func TestServer_Ping(t *testing.T) {
	s := NewServer("rousseau-test", "0.0.0", silentLogger())
	resp := call(t, s, MethodPing, json.RawMessage(`7`), nil)
	assert.Nil(t, resp.Error)
}

func TestServer_DuplicateRegister(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	require.NoError(t, s.Register(ToolSpec{Name: "a", Handler: dummyHandler()}))
	err := s.Register(ToolSpec{Name: "a", Handler: dummyHandler()})
	assert.Error(t, err)
}

func TestServer_EmptyToolName(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	err := s.Register(ToolSpec{Handler: dummyHandler()})
	assert.Error(t, err)
}

func TestServer_MustRegisterPanicsOnDuplicate(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	s.MustRegister(ToolSpec{Name: "a", Handler: dummyHandler()})
	assert.Panics(t, func() { s.MustRegister(ToolSpec{Name: "a", Handler: dummyHandler()}) })
}

func TestServer_ConcurrentServe(t *testing.T) {
	s := NewServer("t", "0", silentLogger())
	s.MustRegister(ToolSpec{Name: "ok", Handler: func(_ context.Context, _ json.RawMessage) ([]Content, error) {
		return TextContent("ok"), nil
	}})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			params, _ := json.Marshal(ToolsCallParams{Name: "ok"})
			resp := call(t, s, MethodToolsCall, []byte(`"`+string(rune('a'+i))+`"`), params)
			assert.Nil(t, resp.Error)
		}(i)
	}
	wg.Wait()
}

func dummyHandler() Handler {
	return func(context.Context, json.RawMessage) ([]Content, error) { return nil, nil }
}
