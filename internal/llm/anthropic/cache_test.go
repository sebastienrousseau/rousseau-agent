package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// hasCacheMarker checks whether a marshaled MessageParam mentions
// `cache_control` — the only reliable signal in the SDK's typed value
// world.
func hasCacheMarker(t *testing.T, m sdk.MessageParam) bool {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return strings.Contains(string(b), `"cache_control"`)
}

func TestApplyCacheMarkers_ZeroIsNoOp(t *testing.T) {
	msgs, err := toSDKMessages([]agent.Message{agent.NewUserText("hello")})
	require.NoError(t, err)
	applyCacheMarkers(msgs, 0)
	assert.False(t, hasCacheMarker(t, msgs[0]))
}

func TestApplyCacheMarkers_MarksLastN(t *testing.T) {
	msgs, err := toSDKMessages([]agent.Message{
		agent.NewUserText("one"),
		agent.NewAssistantText("two"),
		agent.NewUserText("three"),
	})
	require.NoError(t, err)
	require.Len(t, msgs, 3)

	applyCacheMarkers(msgs, 2)

	assert.False(t, hasCacheMarker(t, msgs[0]), "first message must not be marked")
	assert.True(t, hasCacheMarker(t, msgs[1]), "second-to-last must be marked")
	assert.True(t, hasCacheMarker(t, msgs[2]), "last must be marked")
}

func TestApplyCacheMarkers_CapsAtLength(t *testing.T) {
	msgs, err := toSDKMessages([]agent.Message{agent.NewUserText("hi")})
	require.NoError(t, err)
	assert.NotPanics(t, func() { applyCacheMarkers(msgs, 1000) })
	assert.True(t, hasCacheMarker(t, msgs[0]))
}

func TestApplyCacheMarkers_EmptyMessagesIsSafe(t *testing.T) {
	assert.NotPanics(t, func() { applyCacheMarkers(nil, 5) })
	assert.NotPanics(t, func() { applyCacheMarkers([]sdk.MessageParam{}, 5) })
}

func TestMarkLastTextBlock_SkipsToolUseAndMarksText(t *testing.T) {
	// Last block is a tool_use; the marker should walk backward and
	// land on the text block instead.
	msg := sdk.NewUserMessage(
		sdk.NewTextBlock("hello"),
		sdk.NewToolUseBlock("tu-1", []byte(`{}`), "read"),
	)
	markLastTextBlock(&msg)
	assert.True(t, hasCacheMarker(t, msg))
}

func TestMarkLastTextBlock_NoTextBlockIsNoop(t *testing.T) {
	msg := sdk.NewUserMessage(
		sdk.NewToolUseBlock("tu-1", []byte(`{}`), "read"),
	)
	assert.NotPanics(t, func() { markLastTextBlock(&msg) })
	assert.False(t, hasCacheMarker(t, msg))
}
