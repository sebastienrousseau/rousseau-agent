package vertex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

func TestComplete_WithSystemAndTools(t *testing.T) {
	var seen []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body) //nolint:errcheck // test fixture
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write( //nolint:errcheck // test fixture
			[]byte(`{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`))
	}))
	defer srv.Close()

	p, err := New(context.Background(), Config{
		Project: "p", Region: "us-central1", Model: "claude-sonnet-4",
		HTTPClient: injectedClient(srv),
	})
	require.NoError(t, err)
	_, err = p.Complete(context.Background(), agent.Request{
		System:   "You are careful.",
		Messages: []agent.Message{agent.NewUserText("hello")},
	})
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(seen, &raw))
	assert.Equal(t, "You are careful.", raw["system"])
}

func TestComplete_HTTPBuildError(t *testing.T) {
	p, err := New(context.Background(), Config{
		Project: "p", Region: "us-central1", Model: "m",
		HTTPClient: &http.Client{},
	})
	require.NoError(t, err)
	// A cancelled ctx forces the request build to bail before Do.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Complete(ctx, agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	assert.Error(t, err)
}

func TestToVertexContent_ToolBlocks(t *testing.T) {
	cs := []agent.Content{
		{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{
			ID: "t", Name: "read", Input: json.RawMessage(`{"path":"/x"}`),
		}},
		{Kind: agent.ContentToolResult, ToolResult: &agent.ToolResult{
			ToolUseID: "t", Output: "ok",
		}},
	}
	blocks, err := toVertexContent(cs)
	require.NoError(t, err)
	require.Len(t, blocks, 2)
	assert.Equal(t, "tool_use", blocks[0].Type)
}

func TestToVertexContent_MalformedToolInput(t *testing.T) {
	_, err := toVertexContent([]agent.Content{{
		Kind: agent.ContentToolUse,
		ToolUse: &agent.ToolUse{
			ID: "t", Name: "read",
			Input: json.RawMessage(`not json`),
		},
	}})
	assert.ErrorContains(t, err, "tool_use input")
}

func TestToVertexContent_MissingPayloadRejected(t *testing.T) {
	_, err := toVertexContent([]agent.Content{{Kind: agent.ContentToolUse}})
	assert.ErrorContains(t, err, "tool_use content")
	_, err = toVertexContent([]agent.Content{{Kind: agent.ContentToolResult}})
	assert.ErrorContains(t, err, "tool_result content")
}

func TestToVertexContent_UnsupportedKind(t *testing.T) {
	_, err := toVertexContent([]agent.Content{{Kind: agent.ContentKind("mystery")}})
	assert.ErrorContains(t, err, "unsupported")
}

func TestParseVertexResponse_EmptyTextSkipped(t *testing.T) {
	body := []byte(`{"id":"m","type":"message","role":"assistant","content":[
      {"type":"text","text":""},{"type":"text","text":"real"}
    ],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	resp, err := parseVertexResponse(body)
	require.NoError(t, err)
	require.Len(t, resp.Message.Content, 1)
	assert.Equal(t, "real", resp.Message.Content[0].Text)
}

func TestBuildVertexBody_DefaultMaxTokens(t *testing.T) {
	body, err := buildVertexBody(agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	}, 0)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	_, present := raw["max_tokens"]
	assert.True(t, present)
}

func TestBuildVertexBody_ExplicitMaxTokens(t *testing.T) {
	body, err := buildVertexBody(agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	}, 512)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	assert.InDelta(t, 512, raw["max_tokens"], 0.01)
}
