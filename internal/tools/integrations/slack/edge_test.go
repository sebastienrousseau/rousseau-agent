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
)

func TestClient_HTTPBuildError(t *testing.T) {
	c, err := New(Config{BotToken: "x", BaseURL: "http://x", HTTPClient: http.DefaultClient})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = c.postJSON(ctx, "chat.postMessage", map[string]any{"channel": "C1", "text": "hi"}, nil)
	assert.Error(t, err)
}

func TestListChannelsTool_TypesOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "types=private_channel%2Cmpim")
		_, _ = w.Write([]byte(`{"ok":true,"channels":[]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{BotToken: "xoxb", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	tool := NewListChannelsTool(c)
	_, err = tool.Execute(context.Background(),
		json.RawMessage(`{"types":"private_channel,mpim","limit":50}`))
	require.NoError(t, err)
}

func TestPostMessageTool_ThreadTS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "1234.5678", payload["thread_ts"])
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"1.2"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{BotToken: "x", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	tool := NewPostMessageTool(c)
	_, err = tool.Execute(context.Background(),
		json.RawMessage(`{"channel":"C1","text":"reply","thread_ts":"1234.5678"}`))
	require.NoError(t, err)
}

func TestPostMessageTool_BadInputJSON(t *testing.T) {
	c := &Client{}
	tool := NewPostMessageTool(c)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{not json`))
	assert.ErrorContains(t, err, "bad input")
}
