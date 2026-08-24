package builtin_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgbuiltin "github.com/sebastienrousseau/rousseau-agent/pkg/tools/builtin"
)

func TestNewReadTool(t *testing.T) {
	tool := pkgbuiltin.NewReadTool()
	require.NotNil(t, tool)
	assert.Equal(t, "read", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.Equal(t, "object", tool.InputSchema()["type"])
}

func TestNewWriteTool(t *testing.T) {
	tool := pkgbuiltin.NewWriteTool()
	require.NotNil(t, tool)
	assert.Equal(t, "write", tool.Name())
}

func TestNewEditTool(t *testing.T) {
	tool := pkgbuiltin.NewEditTool()
	require.NotNil(t, tool)
	assert.Equal(t, "edit", tool.Name())
}

func TestNewGrepTool(t *testing.T) {
	tool := pkgbuiltin.NewGrepTool(100, 1024*1024)
	require.NotNil(t, tool)
	assert.Equal(t, "grep", tool.Name())
}

func TestNewGrepTool_ZeroValuesUseDefaults(t *testing.T) {
	// Zero-value caps must not panic — the underlying constructor
	// substitutes defaults.
	assert.NotNil(t, pkgbuiltin.NewGrepTool(0, 0))
}

func TestNewBashTool(t *testing.T) {
	tool := pkgbuiltin.NewBashTool(30 * time.Second)
	require.NotNil(t, tool)
	assert.Equal(t, "bash", tool.Name())
}

func TestNewBashTool_ZeroTimeoutUsesDefault(t *testing.T) {
	// Zero timeout must produce a functional tool (underlying default fires).
	tool := pkgbuiltin.NewBashTool(0)
	require.NotNil(t, tool)
	// The tool should still execute correctly.
	_, err := tool.Execute(context.Background(), []byte(`{"command":"true"}`))
	assert.NoError(t, err)
}
