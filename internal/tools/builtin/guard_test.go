package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools/fsguard"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// Every file tool refuses the deny list even with no guard injected:
// the process default applies to library consumers too.
func TestFileTools_DefaultGuardDeniesProc(t *testing.T) {
	ctx := context.Background()
	_, err := NewReadTool().Execute(ctx, mustJSON(t, map[string]string{"path": "/proc/self/environ"}))
	assert.ErrorIs(t, err, fsguard.ErrDenied)
	_, err = NewWriteTool().Execute(ctx, mustJSON(t, map[string]string{"path": "/proc/self/x", "content": "x"}))
	assert.ErrorIs(t, err, fsguard.ErrDenied)
	_, err = NewEditTool().Execute(ctx, mustJSON(t, map[string]string{"path": "/proc/self/x", "old_string": "a", "new_string": "b"}))
	assert.ErrorIs(t, err, fsguard.ErrDenied)
	_, err = NewGrepTool(0, 0).Execute(ctx, mustJSON(t, map[string]string{"path": "/proc", "pattern": "x"}))
	assert.ErrorIs(t, err, fsguard.ErrDenied)
}

func TestFileTools_WorkspaceRootConfinesAllFour(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	g, err := fsguard.New(root, nil)
	require.NoError(t, err)
	ctx := context.Background()

	rt := NewReadTool()
	rt.Guard = g
	wt := NewWriteTool()
	wt.Guard = g
	et := NewEditTool()
	et.Guard = g
	gt := NewGrepTool(0, 0)
	gt.Guard = g

	esc := filepath.Join(outside, "f.txt")
	require.NoError(t, os.WriteFile(esc, []byte("hello"), 0o600))

	_, err = rt.Execute(ctx, mustJSON(t, map[string]string{"path": esc}))
	assert.ErrorIs(t, err, fsguard.ErrOutsideRoot)
	_, err = wt.Execute(ctx, mustJSON(t, map[string]string{"path": esc, "content": "x"}))
	assert.ErrorIs(t, err, fsguard.ErrOutsideRoot)
	_, err = et.Execute(ctx, mustJSON(t, map[string]string{"path": esc, "old_string": "hello", "new_string": "bye"}))
	assert.ErrorIs(t, err, fsguard.ErrOutsideRoot)
	_, err = gt.Execute(ctx, mustJSON(t, map[string]string{"path": outside, "pattern": "hello"}))
	assert.ErrorIs(t, err, fsguard.ErrOutsideRoot)

	// Inside the root the full round-trip works.
	in := filepath.Join(root, "f.txt")
	_, err = wt.Execute(ctx, mustJSON(t, map[string]string{"path": in, "content": "hello"}))
	require.NoError(t, err)
	_, err = et.Execute(ctx, mustJSON(t, map[string]string{"path": in, "old_string": "hello", "new_string": "bye"}))
	require.NoError(t, err)
	got, err := rt.Execute(ctx, mustJSON(t, map[string]string{"path": in}))
	require.NoError(t, err)
	assert.Equal(t, "bye", got)
	out, err := gt.Execute(ctx, mustJSON(t, map[string]string{"path": root, "pattern": "bye"}))
	require.NoError(t, err)
	assert.Contains(t, out, "f.txt:1: bye")
}

func TestReadTool_RefusesNonRegularAndOversized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("device files")
	}
	ctx := context.Background()
	dir := t.TempDir()

	// A directory is not a regular file.
	_, err := NewReadTool().Execute(ctx, mustJSON(t, map[string]string{"path": dir}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")

	// Over the limit by size.
	big := filepath.Join(dir, "big.txt")
	require.NoError(t, os.WriteFile(big, []byte(strings.Repeat("a", 64)), 0o600))
	rt := NewReadTool()
	rt.MaxBytes = 16
	_, err = rt.Execute(ctx, mustJSON(t, map[string]string{"path": big}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "over the 16 byte limit")

	// At the limit is fine.
	rt.MaxBytes = 64
	got, err := rt.Execute(ctx, mustJSON(t, map[string]string{"path": big}))
	require.NoError(t, err)
	assert.Len(t, got, 64)
}

func TestBashTool_ScrubsEnvironment(t *testing.T) {
	t.Setenv("ROUSSEAU_TEST_SECRET", "leak-me")
	t.Setenv("ROUSSEAU_TEST_ALLOWED", "ok")
	ctx := context.Background()

	out, err := NewBashTool(0).Execute(ctx, mustJSON(t, map[string]string{"command": "echo \"s=$ROUSSEAU_TEST_SECRET a=$ROUSSEAU_TEST_ALLOWED p=$PATH\""}))
	require.NoError(t, err)
	assert.Contains(t, out, "s= a= p=")
	assert.NotContains(t, out, "leak-me")

	bt := NewBashTool(0)
	bt.EnvPassthrough = []string{"ROUSSEAU_TEST_ALLOWED"}
	out, err = bt.Execute(ctx, mustJSON(t, map[string]string{"command": "echo \"s=$ROUSSEAU_TEST_SECRET a=$ROUSSEAU_TEST_ALLOWED\""}))
	require.NoError(t, err)
	assert.Contains(t, out, "s= a=ok")
}
