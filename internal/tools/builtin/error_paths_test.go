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

// requireNonRoot skips permission-based fixtures when the test runs as
// root, where the DAC checks they rely on are bypassed.
func requireNonRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission fixture is meaningless as root")
	}
}

// chmodTemp changes path's mode and restores it during cleanup so
// t.TempDir()'s own removal still works.
func chmodTemp(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, mode))
	t.Cleanup(func() { _ = os.Chmod(path, info.Mode()) })
}

// -- bash --------------------------------------------------------------

func TestBashTool_InvalidJSON(t *testing.T) {
	_, err := NewBashTool(0).Execute(context.Background(), json.RawMessage(`{"command":`))
	assert.ErrorContains(t, err, "bash: parse input")
}

func TestBashTool_NonZeroExitSurfacesOutputAndError(t *testing.T) {
	out, err := NewBashTool(0).Execute(context.Background(),
		json.RawMessage(`{"command":"echo before-failure; exit 3"}`))
	require.Error(t, err)
	// A failing command still returns everything it printed — the model
	// needs the output to decide what to do next.
	assert.Contains(t, out, "before-failure")
	assert.Contains(t, err.Error(), "bash: ")
	assert.NotContains(t, err.Error(), "timed out")
}

func TestBashTool_MergesStderrIntoOutput(t *testing.T) {
	out, err := NewBashTool(0).Execute(context.Background(),
		json.RawMessage(`{"command":"echo to-stdout; echo to-stderr 1>&2"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "to-stdout")
	assert.Contains(t, out, "to-stderr")
}

// -- read --------------------------------------------------------------

func TestReadTool_InvalidJSON(t *testing.T) {
	_, err := NewReadTool().Execute(context.Background(), json.RawMessage(`{"path":`))
	assert.ErrorContains(t, err, "read: parse input")
}

func TestReadTool_MissingFile(t *testing.T) {
	_, err := NewReadTool().Execute(context.Background(),
		json.RawMessage(`{"path":"`+filepath.Join(t.TempDir(), "nope.txt")+`"}`))
	assert.ErrorContains(t, err, "read: ")
}

func TestReadTool_SniffsOnlyFirstBlock(t *testing.T) {
	// isLikelyText inspects at most 512 bytes: a NUL past that window
	// is invisible, while one inside it rejects the file.
	dir := t.TempDir()
	for _, tc := range []struct {
		name    string
		body    []byte
		wantErr bool
	}{
		{"nul beyond sniff window", append([]byte(strings.Repeat("a", 600)), 0x00), false},
		{"nul inside sniff window", append([]byte(strings.Repeat("a", 10)), 0x00), true},
		{"large pure text", []byte(strings.Repeat("b", 4096)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_"))
			require.NoError(t, os.WriteFile(p, tc.body, 0o644))
			out, err := NewReadTool().Execute(context.Background(),
				json.RawMessage(`{"path":"`+p+`"}`))
			if tc.wantErr {
				assert.ErrorContains(t, err, "UTF-8 text")
				return
			}
			require.NoError(t, err)
			assert.Len(t, out, len(tc.body))
		})
	}
}

// -- write -------------------------------------------------------------

func TestWriteTool_TargetIsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a-directory")
	require.NoError(t, os.Mkdir(dir, 0o755))
	_, err := NewWriteTool().Execute(context.Background(),
		json.RawMessage(`{"path":"`+dir+`","content":"x"}`))
	assert.ErrorContains(t, err, "write: ")
}

// -- edit --------------------------------------------------------------

func TestEditTool_UnwritableFile(t *testing.T) {
	requireNonRoot(t)
	p := filepath.Join(t.TempDir(), "locked.txt")
	require.NoError(t, os.WriteFile(p, []byte("needle in haystack"), 0o644))
	chmodTemp(t, p, 0o444)

	_, err := NewEditTool().Execute(context.Background(),
		json.RawMessage(`{"path":"`+p+`","old_string":"needle","new_string":"pin"}`))
	assert.ErrorContains(t, err, "edit: write")

	// The original content must be intact after a failed write.
	b, rErr := os.ReadFile(p)
	require.NoError(t, rErr)
	assert.Equal(t, "needle in haystack", string(b))
}

// -- grep --------------------------------------------------------------

func TestGrepTool_MissingRootDegradesToNoMatches(t *testing.T) {
	// WalkDir reports the stat failure through the callback; grep is
	// documented to degrade gracefully rather than error.
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	in, err := json.Marshal(map[string]any{"pattern": "x", "path": missing})
	require.NoError(t, err)
	out, err := NewGrepTool(0, 0).Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "no matches", out)
}

func TestGrepTool_StopsWalkingOnceMaxMatchesReached(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.txt": "needle\n",
		"b.txt": "needle\n",
		"c.txt": "needle\n",
	})
	in, err := json.Marshal(map[string]any{"pattern": "needle", "path": root})
	require.NoError(t, err)
	out, err := NewGrepTool(1, 0).Execute(context.Background(), in)
	require.NoError(t, err)

	assert.Contains(t, out, "a.txt")
	assert.NotContains(t, out, "b.txt", "the walk must stop once the cap is hit")
	assert.NotContains(t, out, "c.txt")
	assert.Contains(t, out, "(truncated at 1 matches)")
}

func TestGrepTool_SkipsUnreadableFile(t *testing.T) {
	requireNonRoot(t)
	root := writeTree(t, map[string]string{
		"readable.txt":   "needle here\n",
		"unreadable.txt": "needle there\n",
	})
	chmodTemp(t, filepath.Join(root, "unreadable.txt"), 0o000)

	in, err := json.Marshal(map[string]any{"pattern": "needle", "path": root})
	require.NoError(t, err)
	out, err := NewGrepTool(0, 0).Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out, "readable.txt")
	assert.NotContains(t, out, "unreadable.txt")
}

func TestGrepTool_SkipsEntriesWhoseStatFails(t *testing.T) {
	requireNonRoot(t)
	// A directory with read but no search permission can be listed, yet
	// lstat-ing its children fails — d.Info() errors mid-walk.
	root := t.TempDir()
	sub := filepath.Join(root, "nosearch")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "hidden.txt"), []byte("needle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "visible.txt"), []byte("needle\n"), 0o644))
	chmodTemp(t, sub, 0o444)

	in, err := json.Marshal(map[string]any{"pattern": "needle", "path": root})
	require.NoError(t, err)
	out, err := NewGrepTool(0, 0).Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out, "visible.txt")
	assert.NotContains(t, out, "hidden.txt")
}
