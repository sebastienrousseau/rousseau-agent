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

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f.txt")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestEditTool_ReplacesUnique(t *testing.T) {
	path := writeTempFile(t, "alpha beta gamma")
	tool := NewEditTool()
	in, err := json.Marshal(map[string]string{
		"path": path, "old_string": "beta", "new_string": "BETA",
	})
	require.NoError(t, err)

	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out, "1 replacement")

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "alpha BETA gamma", string(b))
}

func TestEditTool_NotFound(t *testing.T) {
	path := writeTempFile(t, "alpha")
	tool := NewEditTool()
	in, err := json.Marshal(map[string]string{
		"path": path, "old_string": "missing", "new_string": "x",
	})
	require.NoError(t, err)
	_, err = tool.Execute(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestEditTool_Ambiguous(t *testing.T) {
	path := writeTempFile(t, "foo bar foo")
	tool := NewEditTool()
	in, err := json.Marshal(map[string]string{
		"path": path, "old_string": "foo", "new_string": "FOO",
	})
	require.NoError(t, err)
	_, err = tool.Execute(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not unique")
}

func TestEditTool_MissingFile(t *testing.T) {
	tool := NewEditTool()
	in, err := json.Marshal(map[string]string{
		"path": "/nonexistent/file.txt", "old_string": "a", "new_string": "b",
	})
	require.NoError(t, err)
	_, err = tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestEditTool_RejectsRelative(t *testing.T) {
	tool := NewEditTool()
	in := json.RawMessage(`{"path": "rel.txt", "old_string": "a", "new_string": "b"}`)
	_, err := tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestEditTool_IdenticalStrings(t *testing.T) {
	tool := NewEditTool()
	in := json.RawMessage(`{"path": "/tmp/x", "old_string": "a", "new_string": "a"}`)
	_, err := tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestEditTool_EmptyOld(t *testing.T) {
	tool := NewEditTool()
	in := json.RawMessage(`{"path": "/tmp/x", "old_string": "", "new_string": "b"}`)
	_, err := tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestEditTool_EmptyPath(t *testing.T) {
	tool := NewEditTool()
	in := json.RawMessage(`{"path": "", "old_string": "a", "new_string": "b"}`)
	_, err := tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestEditTool_InvalidJSON(t *testing.T) {
	tool := NewEditTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{`))
	assert.Error(t, err)
}

func TestEditTool_Metadata(t *testing.T) {
	tool := NewEditTool()
	assert.Equal(t, "edit", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.Equal(t, "object", tool.InputSchema()["type"])
}
