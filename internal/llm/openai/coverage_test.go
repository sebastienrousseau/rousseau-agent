package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/openai/openai-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// chatFixture is a minimal, well-formed chat/completions response.
const chatFixture = `{
  "id": "chatcmpl-1",
  "object": "chat.completion",
  "created": 1,
  "model": "gpt-test",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "pong"},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9}
}`

// newTestProvider wires a Provider at an httptest server. The handler
// receives the decoded request body so tests can assert on the wire
// shape the provider actually produced.
func newTestProvider(t *testing.T, cfg Config, inspect func(t *testing.T, body map[string]any)) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		if inspect != nil {
			inspect(t, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatFixture)) //nolint:errcheck // test fixture
	}))
	t.Cleanup(srv.Close)

	cfg.APIKey = "sk-test"
	cfg.BaseURL = srv.URL
	if cfg.Model == "" {
		cfg.Model = "gpt-test"
	}
	p, err := New(cfg)
	require.NoError(t, err)
	return p
}

// TestComplete_SendsMaxTokensAndTools verifies both optional request
// fields land on the wire when configured, and are absent otherwise.
func TestComplete_SendsMaxTokensAndTools(t *testing.T) {
	p := newTestProvider(t, Config{MaxTokens: 321}, func(t *testing.T, body map[string]any) {
		t.Helper()
		assert.EqualValues(t, 321, body["max_tokens"])
		toolList, ok := body["tools"].([]any)
		require.True(t, ok, "tools must be present, got %v", body["tools"])
		require.Len(t, toolList, 1)
		fn := toolList[0].(map[string]any)["function"].(map[string]any)
		assert.Equal(t, "grep", fn["name"])
	})

	resp, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("ping")},
		Tools: []tools.Definition{{
			Name: "grep", Description: "search",
			InputSchema: map[string]any{"type": "object"},
		}},
	})
	require.NoError(t, err)
	require.Len(t, resp.Message.Content, 1)
	assert.Equal(t, "pong", resp.Message.Content[0].Text)
	assert.Equal(t, agent.StopEndTurn, resp.StopReason)
	assert.Equal(t, 7, resp.Usage.InputTokens)
	assert.Equal(t, 2, resp.Usage.OutputTokens)
}

func TestComplete_OmitsMaxTokensAndToolsWhenUnset(t *testing.T) {
	p := newTestProvider(t, Config{}, func(t *testing.T, body map[string]any) {
		t.Helper()
		assert.NotContains(t, body, "max_tokens")
		assert.NotContains(t, body, "tools")
	})
	_, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("ping")},
	})
	require.NoError(t, err)
}

// TestComplete_ConversionErrorShortCircuits proves an unconvertible
// message never reaches the transport: the server handler would fail
// the test if it were hit.
func TestComplete_ConversionErrorShortCircuits(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatFixture)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	p, err := New(Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{{
			Role:    agent.Role("nonsense"),
			Content: []agent.Content{{Kind: agent.ContentText, Text: "x"}},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported role")
	assert.Zero(t, hits, "conversion failure must not issue an HTTP request")
}

func TestComplete_UpstreamErrorIsWrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"nope","type":"invalid_request_error"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	p, err := New(Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
	require.NoError(t, err)
	_, err = p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openai: complete")
}

// TestToSDKMessage_AssistantWithoutToolCalls covers the plain-assistant
// branch (no tool_calls -> simple assistant message).
func TestToSDKMessage_AssistantWithoutToolCalls(t *testing.T) {
	got, err := toSDKMessage(agent.Message{
		Role:    agent.RoleAssistant,
		Content: []agent.Content{{Kind: agent.ContentText, Text: "plain reply"}},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	raw, err := json.Marshal(got[0])
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"role":"assistant"`)
	assert.Contains(t, string(raw), "plain reply")
	assert.NotContains(t, string(raw), "tool_calls")
}

// TestToSDKMessage_SystemRoleInHistory covers the RoleSystem branch of
// toSDKMessage (distinct from the Request.System shortcut).
func TestToSDKMessage_SystemRoleInHistory(t *testing.T) {
	got, err := toSDKMessage(agent.Message{
		Role: agent.RoleSystem,
		Content: []agent.Content{
			{Kind: agent.ContentText, Text: "be brief"},
			{Kind: agent.ContentText, Text: "be kind"},
		},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	raw, err := json.Marshal(got[0])
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"role":"system"`)
	assert.Contains(t, string(raw), "be brief\\nbe kind")
}

// TestUserMessage_SkipsNilImagePayload proves a nil Image block is
// dropped rather than panicking or emitting an empty image part.
func TestUserMessage_SkipsNilImagePayload(t *testing.T) {
	m := userMessage([]agent.Content{
		{Kind: agent.ContentText, Text: "look"},
		{Kind: agent.ContentImage, Image: nil},
		{Kind: agent.ContentImage, Image: &agent.Image{MediaType: "image/png", Data: []byte{1, 2}}},
	})
	raw, err := json.Marshal(m)
	require.NoError(t, err)

	var decoded struct {
		Content []map[string]any `json:"content"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Len(t, decoded.Content, 2, "nil image must be skipped: %s", raw)
	assert.Equal(t, "text", decoded.Content[0]["type"])
	assert.Equal(t, "image_url", decoded.Content[1]["type"])
}

// TestUserMessage_DropsEmptyTextPart keeps the multipart builder from
// emitting zero-length text blocks (rejected by several gateways).
func TestUserMessage_DropsEmptyTextPart(t *testing.T) {
	m := userMessage([]agent.Content{
		{Kind: agent.ContentText, Text: ""},
		{Kind: agent.ContentImage, Image: &agent.Image{MediaType: "image/gif", Data: []byte{9}}},
	})
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	var decoded struct {
		Content []map[string]any `json:"content"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Len(t, decoded.Content, 1)
	assert.Equal(t, "image_url", decoded.Content[0]["type"])
}

func TestFromSDKResponse_RejectsEmptyPayloads(t *testing.T) {
	_, err := fromSDKResponse(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")

	_, err = fromSDKResponse(&sdk.ChatCompletion{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")
}

// TestComplete_ToolCallResponseRoundTrip drives the tool_calls decoding
// branch of fromSDKResponse over a real HTTP round trip.
func TestComplete_ToolCallResponseRoundTrip(t *testing.T) {
	const body = `{
      "id":"c1","object":"chat.completion","created":1,"model":"gpt-test",
      "choices":[{"index":0,"finish_reason":"tool_calls","message":{
        "role":"assistant","content":"",
        "tool_calls":[{"id":"call-9","type":"function",
          "function":{"name":"grep","arguments":"{\"pattern\":\"x\"}"}}]}}],
      "usage":{"prompt_tokens":3,"completion_tokens":4}
    }`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	p, err := New(Config{APIKey: "k", Model: "m", BaseURL: srv.URL})
	require.NoError(t, err)
	resp, err := p.Complete(context.Background(), agent.Request{
		System:   "be terse",
		Messages: []agent.Message{agent.NewUserText("find x")},
	})
	require.NoError(t, err)
	require.Len(t, resp.Message.Content, 1, "empty text content must not become a block")
	require.Equal(t, agent.ContentToolUse, resp.Message.Content[0].Kind)
	tu := resp.Message.Content[0].ToolUse
	require.NotNil(t, tu)
	assert.Equal(t, "call-9", tu.ID)
	assert.Equal(t, "grep", tu.Name)
	assert.JSONEq(t, `{"pattern":"x"}`, string(tu.Input))
	assert.Equal(t, agent.StopToolUse, resp.StopReason)
}
