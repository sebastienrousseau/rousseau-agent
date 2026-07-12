package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// mockCompletionServer returns an httptest.Server that responds to
// POST /chat/completions with the given fixture body.
func mockCompletionServer(t *testing.T, fixture string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixture)) //nolint:errcheck // test fixture
	}))
}

const textOnlyFixture = `{
  "id": "chatcmpl-1",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "gpt-4",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "hi back"},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
}`

const toolCallFixture = `{
  "id": "chatcmpl-2",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "gpt-4",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": null,
      "tool_calls": [{
        "id": "call_123",
        "type": "function",
        "function": {"name": "read", "arguments": "{\"path\":\"/tmp/x\"}"}
      }]
    },
    "finish_reason": "tool_calls"
  }],
  "usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
}`

func TestComplete_TextOnlyResponse(t *testing.T) {
	server := mockCompletionServer(t, textOnlyFixture)
	defer server.Close()

	p, err := New(Config{APIKey: "sk-test", Model: "gpt-4", BaseURL: server.URL})
	require.NoError(t, err)

	resp, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hello")},
	})
	require.NoError(t, err)
	require.Len(t, resp.Message.Content, 1)
	assert.Equal(t, "hi back", resp.Message.Content[0].Text)
	assert.Equal(t, agent.StopEndTurn, resp.StopReason)
	assert.Equal(t, 5, resp.Usage.InputTokens)
	assert.Equal(t, 3, resp.Usage.OutputTokens)
}

func TestComplete_ToolCallResponse(t *testing.T) {
	server := mockCompletionServer(t, toolCallFixture)
	defer server.Close()

	p, err := New(Config{APIKey: "sk-test", Model: "gpt-4", BaseURL: server.URL})
	require.NoError(t, err)

	resp, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("read /tmp/x please")},
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Message.Content)
	// Find the tool_use block.
	var haveToolUse bool
	for _, c := range resp.Message.Content {
		if c.Kind == agent.ContentToolUse && c.ToolUse != nil {
			haveToolUse = true
			assert.Equal(t, "read", c.ToolUse.Name)
			var args map[string]string
			require.NoError(t, json.Unmarshal(c.ToolUse.Input, &args))
			assert.Equal(t, "/tmp/x", args["path"])
		}
	}
	assert.True(t, haveToolUse)
	assert.Equal(t, agent.StopToolUse, resp.StopReason)
}

func TestComplete_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	p, err := New(Config{APIKey: "sk-test", Model: "gpt-4", BaseURL: server.URL})
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	assert.Error(t, err)
}
