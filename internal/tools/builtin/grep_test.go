package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}
	return root
}

func TestGrepTool_FindsMatch(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.txt": "hello world\nfoo bar\n",
		"b.txt": "another line\n",
	})
	tool := NewGrepTool(0, 0)
	in, err := json.Marshal(map[string]any{"pattern": "world", "path": root})
	require.NoError(t, err)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out, "hello world")
}

func TestGrepTool_NoMatches(t *testing.T) {
	root := writeTree(t, map[string]string{"a.txt": "nothing here"})
	tool := NewGrepTool(0, 0)
	in, err := json.Marshal(map[string]any{"pattern": "missing", "path": root})
	require.NoError(t, err)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "no matches", out)
}

func TestGrepTool_IncludeGlob(t *testing.T) {
	root := writeTree(t, map[string]string{
		"keep.go":  "match",
		"skip.txt": "match",
	})
	tool := NewGrepTool(0, 0)
	in, err := json.Marshal(map[string]any{
		"pattern": "match", "path": root, "include": "*.go",
	})
	require.NoError(t, err)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out, "keep.go")
	assert.NotContains(t, out, "skip.txt")
}

func TestGrepTool_IgnoreCase(t *testing.T) {
	root := writeTree(t, map[string]string{"a.txt": "Hello There"})
	tool := NewGrepTool(0, 0)
	in, err := json.Marshal(map[string]any{
		"pattern": "hello", "path": root, "ignore_case": true,
	})
	require.NoError(t, err)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out, "Hello There")
}

func TestGrepTool_TruncateAtMax(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.txt": strings.Repeat("match\n", 50),
	})
	tool := NewGrepTool(5, 0)
	in, err := json.Marshal(map[string]any{"pattern": "match", "path": root})
	require.NoError(t, err)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out, "truncated")
}

func TestGrepTool_SkipsBinary(t *testing.T) {
	root := writeTree(t, map[string]string{
		"bin.dat": "text\x00binary",
		"txt.txt": "text only",
	})
	tool := NewGrepTool(0, 0)
	in, err := json.Marshal(map[string]any{"pattern": "text", "path": root})
	require.NoError(t, err)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out, "txt.txt")
	assert.NotContains(t, out, "bin.dat")
}

func TestGrepTool_SkipsIgnoredDirs(t *testing.T) {
	root := writeTree(t, map[string]string{
		".git/objects/abc":  "match",
		"node_modules/x.js": "match",
		"src/a.go":          "match",
	})
	tool := NewGrepTool(0, 0)
	in, err := json.Marshal(map[string]any{"pattern": "match", "path": root})
	require.NoError(t, err)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out, "a.go")
	assert.NotContains(t, out, ".git")
	assert.NotContains(t, out, "node_modules")
}

func TestGrepTool_SkipsLargeFiles(t *testing.T) {
	root := writeTree(t, map[string]string{
		"big.txt":   strings.Repeat("match\n", 200),
		"small.txt": "match\n",
	})
	tool := NewGrepTool(0, 100) // 100 byte file cap
	in, err := json.Marshal(map[string]any{"pattern": "match", "path": root})
	require.NoError(t, err)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out, "small.txt")
	assert.NotContains(t, out, "big.txt")
}

func TestGrepTool_RejectsRelative(t *testing.T) {
	tool := NewGrepTool(0, 0)
	in := json.RawMessage(`{"pattern": "x", "path": "rel"}`)
	_, err := tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestGrepTool_BadPattern(t *testing.T) {
	tool := NewGrepTool(0, 0)
	in, err := json.Marshal(map[string]any{"pattern": "(", "path": t.TempDir()})
	require.NoError(t, err)
	_, err = tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestGrepTool_BadInclude(t *testing.T) {
	root := writeTree(t, map[string]string{"a.txt": "x"})
	tool := NewGrepTool(0, 0)
	in, err := json.Marshal(map[string]any{
		"pattern": "x", "path": root, "include": "[",
	})
	require.NoError(t, err)
	_, err = tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestGrepTool_MissingPattern(t *testing.T) {
	tool := NewGrepTool(0, 0)
	in := json.RawMessage(`{"path": "/tmp"}`)
	_, err := tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestGrepTool_MissingPath(t *testing.T) {
	tool := NewGrepTool(0, 0)
	in := json.RawMessage(`{"pattern": "x"}`)
	_, err := tool.Execute(context.Background(), in)
	assert.Error(t, err)
}

func TestGrepTool_InvalidJSON(t *testing.T) {
	tool := NewGrepTool(0, 0)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{`))
	assert.Error(t, err)
}

func TestGrepTool_CancelledContext(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.txt": strings.Repeat("line\n", 1000),
	})
	tool := NewGrepTool(0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in, err := json.Marshal(map[string]any{"pattern": "line", "path": root})
	require.NoError(t, err)
	_, _ = tool.Execute(ctx, in)
	// Either returns an error or empty — both acceptable, we're just
	// exercising the cancellation branch.
}

func TestGrepTool_Metadata(t *testing.T) {
	tool := NewGrepTool(0, 0)
	assert.Equal(t, "grep", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.Equal(t, "object", tool.InputSchema()["type"])
}
