package anthropic

import (
	"encoding/json"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
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
	assert.Equal(t, model.StopEndTurn, mapStopReason("end_turn"))
	assert.Equal(t, model.StopToolUse, mapStopReason("tool_use"))
	assert.Equal(t, model.StopMaxTokens, mapStopReason("max_tokens"))
	assert.Equal(t, model.StopOther, mapStopReason("something_unknown"))
	assert.Equal(t, model.StopOther, mapStopReason(""))
}

func TestToSDKMessages_SkipsSystem(t *testing.T) {
	got, err := toSDKMessages([]model.Message{
		{Role: model.RoleSystem, Content: []model.Content{{Kind: model.ContentText, Text: "sys"}}},
		{Role: model.RoleUser, Content: []model.Content{{Kind: model.ContentText, Text: "hi"}}},
	})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestToSDKMessages_RejectsUnknownRole(t *testing.T) {
	_, err := toSDKMessages([]model.Message{
		{Role: model.Role("weird"), Content: []model.Content{{Kind: model.ContentText, Text: "x"}}},
	})
	assert.Error(t, err)
}

func TestToSDKMessages_BubblesContentError(t *testing.T) {
	_, err := toSDKMessages([]model.Message{
		{Role: model.RoleAssistant, Content: []model.Content{
			{Kind: model.ContentToolUse}, // missing ToolUse payload
		}},
	})
	assert.Error(t, err)
}

func TestToSDKContent_AllKinds(t *testing.T) {
	got, err := toSDKContent([]model.Content{
		{Kind: model.ContentText, Text: "hi"},
		{Kind: model.ContentToolUse, ToolUse: &model.ToolUse{
			ID: "1", Name: "n", Input: json.RawMessage(`{}`),
		}},
		{Kind: model.ContentToolResult, ToolResult: &model.ToolResult{
			ToolUseID: "1", Output: "ok",
		}},
	})
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

func TestToSDKContent_UnknownKind(t *testing.T) {
	_, err := toSDKContent([]model.Content{{Kind: model.ContentKind("weird")}})
	assert.Error(t, err)
}

func TestToSDKContent_ToolResultMissingPayload(t *testing.T) {
	_, err := toSDKContent([]model.Content{{Kind: model.ContentToolResult}})
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
	got := toSDKTools(defs, false)
	assert.Len(t, got, 2)
	assert.NotNil(t, got[0].OfTool)
	assert.Equal(t, "one", got[0].OfTool.Name)
	// Without caching enabled, TTL must be zero-value on every tool
	// (any set TTL implies a cache_control marker was applied).
	for i, tt := range got {
		assert.Equal(t, sdk.CacheControlEphemeralTTL(""), tt.OfTool.CacheControl.TTL,
			"tool %d unexpectedly cached", i)
	}

	// With caching, only the last tool carries the 1-hour cache-control
	// marker (Anthropic caches the prefix up to and including this
	// block, so marking the last tool caches the whole tool array +
	// everything before it in the request).
	gotCached := toSDKTools(defs, true)
	assert.Equal(t, sdk.CacheControlEphemeralTTL(""), gotCached[0].OfTool.CacheControl.TTL,
		"non-final tool must not carry a cache marker")
	assert.Equal(t, sdk.CacheControlEphemeralTTLTTL1h,
		gotCached[len(gotCached)-1].OfTool.CacheControl.TTL,
		"final tool must carry a 1-hour cache marker")
}
