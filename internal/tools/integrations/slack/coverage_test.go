package slack

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func TestEveryToolExposesMetadata(t *testing.T) {
	c, err := New(Config{BotToken: "x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, Register(reg, c))
	for _, name := range reg.Names() {
		tool, ok := reg.Get(name)
		require.True(t, ok)
		assert.NotEmpty(t, tool.Description())
		schema := tool.InputSchema()
		assert.Equal(t, "object", schema["type"])
	}
}

func TestGetThreadTool_ValidatesInput(t *testing.T) {
	tool := NewGetThreadTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"channel":"C1"}`))
	assert.ErrorContains(t, err, "required")
}

func TestAddReactionTool_ValidatesInput(t *testing.T) {
	tool := NewAddReactionTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"channel":"C1","name":"x"}`))
	assert.ErrorContains(t, err, "required")
}

func TestListChannelsTool_ValidatesJSON(t *testing.T) {
	tool := NewListChannelsTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{invalid`))
	assert.ErrorContains(t, err, "bad input")
}
