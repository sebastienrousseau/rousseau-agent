package linear

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func TestEveryToolExposesMetadata(t *testing.T) {
	c, err := New(Config{APIKey: "lin_api_x"})
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

func TestGetIssueTool_ValidatesInput(t *testing.T) {
	tool := NewGetIssueTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.ErrorContains(t, err, "required")
}

func TestUpdateIssueTool_ValidatesInput(t *testing.T) {
	tool := NewUpdateIssueTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"title":"new"}`))
	assert.ErrorContains(t, err, "required")
}

func TestListIssuesTool_HandlesEmptyInput(t *testing.T) {
	c, err := New(Config{APIKey: "lin_api_x"})
	require.NoError(t, err)
	tool := NewListIssuesTool(c)
	// Nil input JSON is allowed — every filter is optional.
	_, err = tool.Execute(context.Background(), nil)
	// Will fail on the wire call (no test server), but bad-input parse
	// must not fire.
	if err != nil {
		assert.NotContains(t, err.Error(), "bad input")
	}
}
