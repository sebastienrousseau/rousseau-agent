package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadTool_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	require.NoError(t, os.WriteFile(path, []byte("hi"), 0o600))

	tool := NewReadTool()
	in, err := json.Marshal(map[string]string{"path": path})
	require.NoError(t, err)

	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "hi", out)
}

func TestReadTool_RejectsRelative(t *testing.T) {
	tool := NewReadTool()
	in := json.RawMessage(`{"path": "relative/path"}`)
	_, err := tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestReadTool_MissingPath(t *testing.T) {
	tool := NewReadTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.Error(t, err)
}

func TestReadTool_RejectsBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.bin")
	require.NoError(t, os.WriteFile(path, []byte{0x00, 0x01, 0x02}, 0o600))

	tool := NewReadTool()
	in, err := json.Marshal(map[string]string{"path": path})
	require.NoError(t, err)
	_, err = tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestReadTool_Metadata(t *testing.T) {
	tool := NewReadTool()
	assert.Equal(t, "read", tool.Name())
	assert.NotEmpty(t, tool.Description())
	schema := tool.InputSchema()
	assert.Equal(t, "object", schema["type"])
}
