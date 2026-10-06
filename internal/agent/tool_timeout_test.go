package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// blockingTool waits for its context to end, like a hung bash or MCP
// call.
type blockingTool struct{}

func (blockingTool) Name() string                { return "hang" }
func (blockingTool) Description() string         { return "hangs" }
func (blockingTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (blockingTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// A single hung tool must not consume the whole turn budget: it is
// cut off at ToolTimeout and reported to the model as an error
// result, and the turn continues.
func TestTurn_ToolTimeoutIsBoundedAndReported(t *testing.T) {
	prov := &stubProvider{responses: []Response{
		{
			Message: Message{Role: RoleAssistant, Content: []Content{
				{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "h1", Name: "hang", Input: json.RawMessage(`{}`)}},
			}},
			StopReason: StopToolUse,
		},
		{
			Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "gave up"}}},
			StopReason: StopEndTurn,
		},
	}}
	reg := tools.NewRegistry()
	reg.MustRegister(blockingTool{})
	a := New(prov, reg, silentLogger(), Options{ToolTimeout: 30 * time.Millisecond})
	s := NewSession("x")
	s.Append(NewUserText("go"))

	start := time.Now()
	final, err := a.Turn(context.Background(), s)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, "gave up", final.Content[0].Text)

	res := s.Messages[2].Content[0].ToolResult
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Output, ErrToolTimeout.Error())
	assert.Contains(t, res.Output, "30ms")
}

func TestToolTimeout_DefaultsWhenUnset(t *testing.T) {
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(), Options{})
	assert.Equal(t, defaultToolTimeout, a.toolTimeout())
	a = New(&stubProvider{}, tools.NewRegistry(), silentLogger(), Options{ToolTimeout: time.Second})
	assert.Equal(t, time.Second, a.toolTimeout())
}
