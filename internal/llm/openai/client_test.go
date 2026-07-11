package openai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func TestNew_RequiresAPIKey(t *testing.T) {
	_, err := New(Config{Model: "gpt-4"})
	assert.Error(t, err)
}

func TestNew_RequiresModel(t *testing.T) {
	_, err := New(Config{APIKey: "sk-test"})
	assert.Error(t, err)
}

func TestNew_DefaultsName(t *testing.T) {
	p, err := New(Config{APIKey: "sk-test", Model: "gpt-4"})
	require.NoError(t, err)
	assert.Equal(t, "openai", p.Name())
}

func TestNew_ExplicitName(t *testing.T) {
	p, err := New(Config{APIKey: "sk-test", Model: "gpt-4", Name: "openrouter"})
	require.NoError(t, err)
	assert.Equal(t, "openrouter", p.Name())
}

func TestCollectText_Combines(t *testing.T) {
	got := collectText([]agent.Content{
		{Kind: agent.ContentText, Text: "a"},
		{Kind: agent.ContentText, Text: "b"},
		{Kind: agent.ContentToolUse},
	})
	assert.Equal(t, "a\nb", got)
}

func TestCollectText_Empty(t *testing.T) {
	assert.Equal(t, "", collectText(nil))
}

func TestToSDKMessages_SkipsSystemWhenPassed(t *testing.T) {
	got, err := toSDKMessages("hi system", []agent.Message{
		agent.NewUserText("hello"),
	})
	require.NoError(t, err)
	assert.Len(t, got, 2) // system + user
}

func TestToSDKMessages_UserRoundtrip(t *testing.T) {
	got, err := toSDKMessages("", []agent.Message{agent.NewUserText("hi")})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestToSDKMessages_AssistantWithToolCall(t *testing.T) {
	msg := agent.Message{
		Role: agent.RoleAssistant,
		Content: []agent.Content{
			{Kind: agent.ContentText, Text: "let me look"},
			{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{
				ID: "call-1", Name: "grep", Input: []byte(`{"pattern":"x"}`),
			}},
		},
	}
	got, err := toSDKMessages("", []agent.Message{msg})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestToSDKMessages_ToolResultAsSeparateMessage(t *testing.T) {
	toolResultMsg := agent.Message{
		Role: agent.Role("tool"),
		Content: []agent.Content{
			{Kind: agent.ContentToolResult, ToolResult: &agent.ToolResult{
				ToolUseID: "call-1", Output: "match!",
			}},
		},
	}
	got, err := toSDKMessages("", []agent.Message{toolResultMsg})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestToSDKMessages_UnknownRoleErrors(t *testing.T) {
	_, err := toSDKMessages("", []agent.Message{
		{Role: agent.Role("weird"), Content: []agent.Content{{Kind: agent.ContentText, Text: "x"}}},
	})
	assert.Error(t, err)
}

func TestToSDKTools(t *testing.T) {
	got := toSDKTools([]tools.Definition{
		{Name: "read", Description: "read a file", InputSchema: map[string]any{"type": "object"}},
		{Name: "grep", Description: "search", InputSchema: map[string]any{"type": "object"}},
	})
	assert.Len(t, got, 2)
	assert.Equal(t, "read", got[0].Function.Name)
}

func TestMapFinishReason(t *testing.T) {
	assert.Equal(t, agent.StopEndTurn, mapFinishReason("stop"))
	assert.Equal(t, agent.StopToolUse, mapFinishReason("tool_calls"))
	assert.Equal(t, agent.StopMaxTokens, mapFinishReason("length"))
	assert.Equal(t, agent.StopOther, mapFinishReason("weird"))
	assert.Equal(t, agent.StopOther, mapFinishReason(""))
}
