package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/toolcontext"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// systemProbeTool records the system prompt a tool sees on its ctx.
type systemProbeTool struct{ seen *string }

func (systemProbeTool) Name() string                { return "probe" }
func (systemProbeTool) Description() string         { return "probe" }
func (systemProbeTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (p systemProbeTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	*p.seen = toolcontext.SystemPrompt(ctx)
	return "ok", nil
}

// Tools that spawn sub-agents need the turn's system prompt so the
// sub-agents inherit it rather than running with none.
func TestTurn_ToolContextCarriesSystemPrompt(t *testing.T) {
	prov := &stubProvider{responses: []Response{
		{
			Message: Message{Role: RoleAssistant, Content: []Content{
				{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "p1", Name: "probe", Input: json.RawMessage(`{}`)}},
			}},
			StopReason: StopToolUse,
		},
		{
			Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "done"}}},
			StopReason: StopEndTurn,
		},
	}}
	var seen string
	reg := tools.NewRegistry()
	reg.MustRegister(systemProbeTool{seen: &seen})
	a := New(prov, reg, silentLogger(), Options{SystemPrompt: "operator rules"})
	s := NewSession("x")
	s.Append(NewUserText("go"))

	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, "operator rules", seen)
}
