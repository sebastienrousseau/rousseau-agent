package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBashTool_RunsEcho(t *testing.T) {
	tool := NewBashTool(2 * time.Second)
	in := json.RawMessage(`{"command": "printf hello"}`)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "hello", out)
}

func TestBashTool_Timeout(t *testing.T) {
	tool := NewBashTool(50 * time.Millisecond)
	in := json.RawMessage(`{"command": "sleep 2"}`)
	_, err := tool.Execute(context.Background(), in)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "timed out") || strings.Contains(err.Error(), "signal:"))
}

func TestBashTool_MissingCommand(t *testing.T) {
	tool := NewBashTool(0)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.Error(t, err)
}

func TestBashTool_Metadata(t *testing.T) {
	tool := NewBashTool(0)
	assert.Equal(t, "bash", tool.Name())
	assert.NotEmpty(t, tool.Description())
	schema := tool.InputSchema()
	assert.Equal(t, "object", schema["type"])
}
