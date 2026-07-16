package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(Config{BotToken: "xoxb-test", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	return c
}

func TestNew_TokenRequired(t *testing.T) {
	t.Setenv(EnvToken, "")
	_, err := New(Config{})
	assert.Error(t, err)
}

func TestNew_EnvFallback(t *testing.T) {
	t.Setenv(EnvToken, "xoxb-env")
	c, err := New(Config{})
	require.NoError(t, err)
	assert.Equal(t, "xoxb-env", c.token)
}

func TestPostMessageTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat.postMessage", r.URL.Path)
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "C1", payload["channel"])
		assert.Equal(t, "hi", payload["text"])
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"1.2"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewPostMessageTool(newTestClient(t, srv))
	assert.Equal(t, "slack_post_message", tool.Name())
	assert.NotEmpty(t, tool.InputSchema())
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"channel":"C1","text":"hi"}`))
	require.NoError(t, err)
	assert.Contains(t, out, `"ts":"1.2"`)
}

func TestPostMessageTool_ErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"not_in_channel"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewPostMessageTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"channel":"C1","text":"hi"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not_in_channel")
}

func TestPostMessageTool_ValidatesInput(t *testing.T) {
	tool := NewPostMessageTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"channel":""}`))
	assert.ErrorContains(t, err, "required")
}

func TestGetThreadTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/conversations.replies", r.URL.Path)
		assert.Equal(t, "C1", r.URL.Query().Get("channel"))
		_, _ = w.Write([]byte(`{"ok":true,"messages":[]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewGetThreadTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"channel":"C1","thread_ts":"1.2"}`))
	require.NoError(t, err)
}

func TestAddReactionTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/reactions.add", r.URL.Path)
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		assert.Contains(t, string(body), "name=thumbsup")
		_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewAddReactionTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"channel":"C1","timestamp":"1.2","name":"thumbsup"}`))
	require.NoError(t, err)
}

func TestListChannelsTool_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/conversations.list", r.URL.Path)
		assert.Equal(t, "public_channel", r.URL.Query().Get("types"))
		_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C1","name":"general"}]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewListChannelsTool(newTestClient(t, srv))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Contains(t, out, "general")
}

func TestListChannelsTool_EmptyInputWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"channels":[]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	tool := NewListChannelsTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(nil))
	require.NoError(t, err)
}

func TestClient_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	tool := NewPostMessageTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"channel":"C1","text":"x"}`))
	assert.ErrorContains(t, err, "500")
}

func TestRegister_AllTools(t *testing.T) {
	c, err := New(Config{BotToken: "x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, Register(reg, c))
	assert.Len(t, reg.Names(), 4)
}
