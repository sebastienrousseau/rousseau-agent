package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// TestComplete_WithSystemAndCache exercises the system-prompt + cache-
// marker branches of Complete (the 57.9% → 90+% jump).
func TestComplete_WithSystemAndCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r.Body) //nolint:errcheck // test fixture
		var raw map[string]any
		require.NoError(t, json.Unmarshal(body, &raw))
		sys, _ := raw["system"].([]any)
		require.NotEmpty(t, sys, "system field should be present")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completeFixture)) //nolint:errcheck // test fixture
	}))
	defer server.Close()

	p := &Provider{
		client: sdk.NewClient(
			option.WithAPIKey("sk-test"),
			option.WithBaseURL(server.URL),
		),
		cfg: Config{APIKey: "sk-test", Model: "claude-sonnet-4-6", MaxTokens: 4096},
	}
	_, err := p.Complete(context.Background(), agent.Request{
		System:            "You are careful.",
		CacheableMessages: 1,
		Messages:          []agent.Message{agent.NewUserText("hello")},
	})
	require.NoError(t, err)
}

// TestComplete_WithToolDefinitions exercises the toSDKTools path.
func TestComplete_WithToolDefinitions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r.Body) //nolint:errcheck // test fixture
		assert.Contains(t, string(body), `"tools":`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completeFixture)) //nolint:errcheck // test fixture
	}))
	defer server.Close()

	p := &Provider{
		client: sdk.NewClient(option.WithAPIKey("k"), option.WithBaseURL(server.URL)),
		cfg:    Config{APIKey: "k", Model: "m", MaxTokens: 100},
	}
	_, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("go")},
		Tools: []tools.Definition{
			{Name: "read", Description: "read a file", InputSchema: map[string]any{
				"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
			}},
		},
	})
	require.NoError(t, err)
}

func TestComplete_UpstreamErrorSurfaces(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()
	p := &Provider{
		client: sdk.NewClient(option.WithAPIKey("k"), option.WithBaseURL(server.URL)),
		cfg:    Config{APIKey: "k", Model: "m", MaxTokens: 100},
	}
	_, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "complete")
}

// TestToSDKMessages_HandlesToolBlocks drives the branches of
// toSDKMessages that previously showed 0%.
func TestToSDKMessages_HandlesToolBlocks(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleAssistant, Content: []agent.Content{
			{Kind: agent.ContentText, Text: "let me look"},
			{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{
				ID: "t1", Name: "read", Input: json.RawMessage(`{"path":"/x"}`),
			}},
		}},
		{Role: agent.RoleUser, Content: []agent.Content{
			{Kind: agent.ContentToolResult, ToolResult: &agent.ToolResult{
				ToolUseID: "t1", Output: "hello", IsError: false,
			}},
		}},
	}
	sdkMsgs, err := toSDKMessages(msgs)
	require.NoError(t, err)
	assert.Len(t, sdkMsgs, 2)
}

// TestToSDKContent_UnsupportedKind covers the default-error branch.
func TestToSDKContent_UnsupportedKind(t *testing.T) {
	_, err := toSDKContent([]agent.Content{{Kind: agent.ContentKind("mystery")}})
	assert.Error(t, err)
}

// TestFromSDKResponse_MultiBlock exercises assembling multi-block
// text + tool_use replies.
func TestFromSDKResponse_MultiBlock(t *testing.T) {
	raw := `{"id":"m","type":"message","role":"assistant","content":[
      {"type":"text","text":"first"},
      {"type":"tool_use","id":"t1","name":"read","input":{"path":"/x"}}
    ],"model":"m","stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(raw)) //nolint:errcheck // test fixture
	}))
	defer server.Close()
	p := &Provider{
		client: sdk.NewClient(option.WithAPIKey("k"), option.WithBaseURL(server.URL)),
		cfg:    Config{APIKey: "k", Model: "m", MaxTokens: 100},
	}
	resp, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("go")},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Message.Content, 2)
	assert.Equal(t, agent.StopToolUse, resp.StopReason)
}

// TestMapStopReason_AllVariants pins every branch.
func TestMapStopReason_AllVariants(t *testing.T) {
	cases := map[string]agent.StopReason{
		"end_turn":   agent.StopEndTurn,
		"tool_use":   agent.StopToolUse,
		"max_tokens": agent.StopMaxTokens,
		"unknown":    agent.StopOther,
		"":           agent.StopOther,
	}
	for input, want := range cases {
		assert.Equal(t, want, mapStopReason(input), input)
	}
}

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	return io.ReadAll(readerAdapter{r})
}

// readerAdapter turns the minimal Read method set into an io.Reader
// suitable for io.ReadAll.
type readerAdapter struct {
	inner interface{ Read([]byte) (int, error) }
}

func (r readerAdapter) Read(p []byte) (int, error) { return r.inner.Read(p) }

// silence unused import when only completeFixture is referenced.
var _ = strings.HasSuffix
