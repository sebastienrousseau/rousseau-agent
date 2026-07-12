package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// stubInvoke satisfies InvokeAPI for tests.
type stubInvoke struct {
	body []byte
	err  error
	seen []byte
}

func (s *stubInvoke) InvokeModel(_ context.Context, in *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	s.seen = in.Body
	if s.err != nil {
		return nil, s.err
	}
	return &bedrockruntime.InvokeModelOutput{Body: s.body}, nil
}

func TestNew_RequiresRegion(t *testing.T) {
	_, err := New(context.Background(), Config{Model: "m"})
	assert.Error(t, err)
}

func TestNew_RequiresModel(t *testing.T) {
	_, err := New(context.Background(), Config{Region: "us-west-2"})
	assert.Error(t, err)
}

func TestNew_UsesInjectedRuntime(t *testing.T) {
	stub := &stubInvoke{}
	p, err := New(context.Background(), Config{Region: "us-west-2", Model: "m", Runtime: stub})
	require.NoError(t, err)
	assert.Equal(t, "bedrock", p.Name())
}

func TestComplete_HappyPath(t *testing.T) {
	stub := &stubInvoke{
		body: []byte(`{
      "id":"msg_01","type":"message","role":"assistant",
      "stop_reason":"end_turn",
      "content":[{"type":"text","text":"hi there"}],
      "usage":{"input_tokens":10,"output_tokens":2}
    }`),
	}
	p, err := New(context.Background(), Config{Region: "us-west-2", Model: "m", Runtime: stub})
	require.NoError(t, err)

	resp, err := p.Complete(context.Background(), agent.Request{
		System:   "you are helpful",
		Messages: []agent.Message{agent.NewUserText("hello")},
	})
	require.NoError(t, err)
	require.Len(t, resp.Message.Content, 1)
	assert.Equal(t, "hi there", resp.Message.Content[0].Text)
	assert.Equal(t, agent.StopEndTurn, resp.StopReason)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 2, resp.Usage.OutputTokens)

	// Verify what we sent Bedrock.
	var sent bedrockRequest
	require.NoError(t, json.NewDecoder(strings.NewReader(string(stub.seen))).Decode(&sent))
	assert.Equal(t, "bedrock-2023-05-31", sent.AnthropicVersion)
	assert.Equal(t, "you are helpful", sent.System)
	assert.Len(t, sent.Messages, 1)
	assert.Equal(t, "user", sent.Messages[0].Role)
}

func TestComplete_ToolUseInResponse(t *testing.T) {
	stub := &stubInvoke{
		body: []byte(`{
      "type":"message","stop_reason":"tool_use",
      "content":[{"type":"tool_use","id":"toolu_1","name":"grep","input":{"pattern":"x"}}],
      "usage":{"input_tokens":5,"output_tokens":3}
    }`),
	}
	p, err := New(context.Background(), Config{Region: "us-west-2", Model: "m", Runtime: stub})
	require.NoError(t, err)

	resp, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("please grep")},
	})
	require.NoError(t, err)
	require.Len(t, resp.Message.Content, 1)
	assert.Equal(t, agent.ContentToolUse, resp.Message.Content[0].Kind)
	assert.Equal(t, "grep", resp.Message.Content[0].ToolUse.Name)
	assert.Equal(t, agent.StopToolUse, resp.StopReason)
}

func TestComplete_ClientError(t *testing.T) {
	stub := &stubInvoke{err: errors.New("throttled")}
	p, err := New(context.Background(), Config{Region: "us-west-2", Model: "m", Runtime: stub})
	require.NoError(t, err)
	_, err = p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	assert.Error(t, err)
}

func TestBuildBedrockBody_ToolResultMessages(t *testing.T) {
	req := agent.Request{
		Messages: []agent.Message{
			{Role: agent.RoleUser, Content: []agent.Content{
				{Kind: agent.ContentToolResult, ToolResult: &agent.ToolResult{
					ToolUseID: "call-1", Output: "matched", IsError: false,
				}},
			}},
		},
	}
	body, err := buildBedrockBody(req, 100)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"tool_use_id":"call-1"`)
	assert.Contains(t, string(body), `"content":"matched"`)
}

func TestBuildBedrockBody_ToolUseInAssistant(t *testing.T) {
	req := agent.Request{
		Messages: []agent.Message{{
			Role: agent.RoleAssistant, Content: []agent.Content{
				{Kind: agent.ContentText, Text: "let me look"},
				{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{
					ID: "tu-1", Name: "grep", Input: json.RawMessage(`{"pattern":"foo"}`),
				}},
			},
		}},
	}
	body, err := buildBedrockBody(req, 100)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"tool_use"`)
	assert.Contains(t, string(body), `"grep"`)
	assert.Contains(t, string(body), `"foo"`)
}

func TestBuildBedrockBody_SkipsSystemRole(t *testing.T) {
	req := agent.Request{
		Messages: []agent.Message{
			{Role: agent.RoleSystem, Content: []agent.Content{{Kind: agent.ContentText, Text: "sys"}}},
			agent.NewUserText("hi"),
		},
	}
	body, err := buildBedrockBody(req, 100)
	require.NoError(t, err)
	var sent bedrockRequest
	require.NoError(t, json.NewDecoder(strings.NewReader(string(body))).Decode(&sent))
	assert.Len(t, sent.Messages, 1)
}

func TestBuildBedrockBody_BadToolInputErrors(t *testing.T) {
	req := agent.Request{
		Messages: []agent.Message{{
			Role: agent.RoleAssistant, Content: []agent.Content{
				{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{
					ID: "1", Name: "n", Input: json.RawMessage(`not json`),
				}},
			},
		}},
	}
	_, err := buildBedrockBody(req, 100)
	assert.Error(t, err)
}

func TestParseBedrockResponse_MalformedJSON(t *testing.T) {
	_, err := parseBedrockResponse([]byte(`not json`))
	assert.Error(t, err)
}

func TestMapStop(t *testing.T) {
	assert.Equal(t, agent.StopEndTurn, mapStop("end_turn"))
	assert.Equal(t, agent.StopToolUse, mapStop("tool_use"))
	assert.Equal(t, agent.StopMaxTokens, mapStop("max_tokens"))
	assert.Equal(t, agent.StopOther, mapStop("weird"))
}

// discardReader is a small helper used to swallow response bodies in
// tests that don't care about them.
type discardReader struct{}

func (discardReader) Read(_ []byte) (int, error) { return 0, io.EOF }
