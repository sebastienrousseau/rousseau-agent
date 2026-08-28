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

func TestWriteTool_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "out.txt")
	tool := NewWriteTool()

	in, err := json.Marshal(map[string]string{"path": path, "content": "hello"})
	require.NoError(t, err)

	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out, "5 bytes")

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))
}

func TestWriteTool_RejectsRelative(t *testing.T) {
	tool := NewWriteTool()
	in := json.RawMessage(`{"path": "rel.txt", "content": "x"}`)
	_, err := tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestWriteTool_MissingPath(t *testing.T) {
	tool := NewWriteTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"content": "x"}`))
	assert.Error(t, err)
}

func TestWriteTool_InvalidJSON(t *testing.T) {
	tool := NewWriteTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{not json`))
	assert.Error(t, err)
}

func TestWriteTool_Metadata(t *testing.T) {
	tool := NewWriteTool()
	assert.Equal(t, "write", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.Equal(t, "object", tool.InputSchema()["type"])
}

func TestWriteTool_MkdirFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	// Try to write under a path where an ancestor is a file — MkdirAll fails.
	target := filepath.Join(blocker, "sub", "out.txt")
	tool := NewWriteTool()
	in, err := json.Marshal(map[string]string{"path": target, "content": "x"})
	require.NoError(t, err)
	_, err = tool.Execute(context.Background(), in)
	assert.Error(t, err)
}
