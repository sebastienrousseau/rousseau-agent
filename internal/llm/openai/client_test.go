package openai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
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
	got := collectText([]model.Content{
		{Kind: model.ContentText, Text: "a"},
		{Kind: model.ContentText, Text: "b"},
		{Kind: model.ContentToolUse},
	})
	assert.Equal(t, "a\nb", got)
}

func TestCollectText_Empty(t *testing.T) {
	assert.Equal(t, "", collectText(nil))
}

func TestToSDKMessages_SkipsSystemWhenPassed(t *testing.T) {
	got, err := toSDKMessages("hi system", []model.Message{
		model.NewUserText("hello"),
	})
	require.NoError(t, err)
	assert.Len(t, got, 2) // system + user
}

func TestToSDKMessages_UserRoundtrip(t *testing.T) {
	got, err := toSDKMessages("", []model.Message{model.NewUserText("hi")})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestToSDKMessages_AssistantWithToolCall(t *testing.T) {
	msg := model.Message{
		Role: model.RoleAssistant,
		Content: []model.Content{
			{Kind: model.ContentText, Text: "let me look"},
			{Kind: model.ContentToolUse, ToolUse: &model.ToolUse{
				ID: "call-1", Name: "grep", Input: []byte(`{"pattern":"x"}`),
			}},
		},
	}
	got, err := toSDKMessages("", []model.Message{msg})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestToSDKMessages_ToolResultAsSeparateMessage(t *testing.T) {
	toolResultMsg := model.Message{
		Role: model.Role("tool"),
		Content: []model.Content{
			{Kind: model.ContentToolResult, ToolResult: &model.ToolResult{
				ToolUseID: "call-1", Output: "match!",
			}},
		},
	}
	got, err := toSDKMessages("", []model.Message{toolResultMsg})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestToSDKMessages_UnknownRoleErrors(t *testing.T) {
	_, err := toSDKMessages("", []model.Message{
		{Role: model.Role("weird"), Content: []model.Content{{Kind: model.ContentText, Text: "x"}}},
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
	assert.Equal(t, model.StopEndTurn, mapFinishReason("stop"))
	assert.Equal(t, model.StopToolUse, mapFinishReason("tool_calls"))
	assert.Equal(t, model.StopMaxTokens, mapFinishReason("length"))
	assert.Equal(t, model.StopOther, mapFinishReason("weird"))
	assert.Equal(t, model.StopOther, mapFinishReason(""))
}
