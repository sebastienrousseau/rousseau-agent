package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func TestNew_RequiresAPIKey(t *testing.T) {
	_, err := New(Config{})
	assert.Error(t, err)
}

func TestNew_AppliesDefaults(t *testing.T) {
	p, err := New(Config{APIKey: "x"})
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", p.cfg.Model)
	assert.Equal(t, int64(4096), p.cfg.MaxTokens)
}

func TestNew_KeepsExplicitValues(t *testing.T) {
	p, err := New(Config{APIKey: "x", Model: "custom", MaxTokens: 128})
	require.NoError(t, err)
	assert.Equal(t, "custom", p.cfg.Model)
	assert.Equal(t, int64(128), p.cfg.MaxTokens)
}

func TestName(t *testing.T) {
	p, err := New(Config{APIKey: "x"})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", p.Name())
}

func TestMapStopReason(t *testing.T) {
	assert.Equal(t, agent.StopEndTurn, mapStopReason("end_turn"))
	assert.Equal(t, agent.StopToolUse, mapStopReason("tool_use"))
	assert.Equal(t, agent.StopMaxTokens, mapStopReason("max_tokens"))
	assert.Equal(t, agent.StopOther, mapStopReason("something_unknown"))
	assert.Equal(t, agent.StopOther, mapStopReason(""))
}

func TestToSDKMessages_SkipsSystem(t *testing.T) {
	got, err := toSDKMessages([]agent.Message{
		{Role: agent.RoleSystem, Content: []agent.Content{{Kind: agent.ContentText, Text: "sys"}}},
		{Role: agent.RoleUser, Content: []agent.Content{{Kind: agent.ContentText, Text: "hi"}}},
	})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestToSDKMessages_RejectsUnknownRole(t *testing.T) {
	_, err := toSDKMessages([]agent.Message{
		{Role: agent.Role("weird"), Content: []agent.Content{{Kind: agent.ContentText, Text: "x"}}},
	})
	assert.Error(t, err)
}

func TestToSDKMessages_BubblesContentError(t *testing.T) {
	_, err := toSDKMessages([]agent.Message{
		{Role: agent.RoleAssistant, Content: []agent.Content{
			{Kind: agent.ContentToolUse}, // missing ToolUse payload
		}},
	})
	assert.Error(t, err)
}

func TestToSDKContent_AllKinds(t *testing.T) {
	got, err := toSDKContent([]agent.Content{
		{Kind: agent.ContentText, Text: "hi"},
		{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{
			ID: "1", Name: "n", Input: json.RawMessage(`{}`),
		}},
		{Kind: agent.ContentToolResult, ToolResult: &agent.ToolResult{
			ToolUseID: "1", Output: "ok",
		}},
	})
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

func TestToSDKContent_UnknownKind(t *testing.T) {
	_, err := toSDKContent([]agent.Content{{Kind: agent.ContentKind("weird")}})
	assert.Error(t, err)
}

func TestToSDKContent_ToolResultMissingPayload(t *testing.T) {
	_, err := toSDKContent([]agent.Content{{Kind: agent.ContentToolResult}})
	assert.Error(t, err)
}

func TestToSDKTools_ProducesToolUnionPerDef(t *testing.T) {
	defs := []tools.Definition{
		{
			Name: "one", Description: "d1",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"x": map[string]any{"type": "string"},
				},
			},
		},
		{Name: "two", Description: "d2", InputSchema: map[string]any{"type": "object"}},
	}
	got := toSDKTools(defs)
	assert.Len(t, got, 2)
	assert.NotNil(t, got[0].OfTool)
	assert.Equal(t, "one", got[0].OfTool.Name)
}
