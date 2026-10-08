//go:build unix

package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools/fsguard"
)

func grepIn(t *testing.T, g *fsguard.Guard, path, pattern string) string {
	t.Helper()
	tool := NewGrepTool(0, 0)
	tool.Guard = g
	in, err := json.Marshal(map[string]string{"path": path, "pattern": pattern})
	require.NoError(t, err)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	return out
}

// A search rooted at an allowed directory must not descend into a
// denied one: grep from $HOME never reads ~/.ssh.
func TestGrep_DoesNotDescendIntoDeniedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ssh", "id_test"), []byte("SECRET-KEY-MARKER"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, "notes.txt"), []byte("ordinary MARKER"), 0o600))
	g, err := fsguard.New("", nil)
	require.NoError(t, err)

	out := grepIn(t, g, home, "MARKER")
	assert.Contains(t, out, "notes.txt")
	assert.NotContains(t, out, "SECRET-KEY")
}

// A symlinked file inside the workspace pointing outside it is never
// opened.
func TestGrep_SkipsSymlinkToOutside(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("OUTSIDE-MARKER"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(ws, "link")))
	g, err := fsguard.New(ws, nil)
	require.NoError(t, err)
	assert.Equal(t, "no matches", grepIn(t, g, ws, "OUTSIDE-MARKER"))
}

// A FIFO in the tree neither hangs grep nor read.
func TestGrepAndRead_DoNotBlockOnFIFO(t *testing.T) {
	ws := t.TempDir()
	fifo := filepath.Join(ws, "pipe")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	g, err := fsguard.New(ws, nil)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = grepIn(t, g, ws, "x")
		r := NewReadTool()
		r.Guard = g
		in, _ := json.Marshal(map[string]string{"path": fifo}) //nolint:errcheck // fixed input
		_, rerr := r.Execute(context.Background(), in)
		assert.Error(t, rerr, "a FIFO is not a regular file")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("grep or read blocked on a FIFO")
	}
}

func TestGrep_TotalOutputCapped(t *testing.T) {
	ws := t.TempDir()
	line := strings.Repeat("y", 400) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(ws, "big.txt"), []byte(strings.Repeat(line, 190)), 0o600))
	g, err := fsguard.New(ws, nil)
	require.NoError(t, err)
	out := grepIn(t, g, ws, "y")
	assert.LessOrEqual(t, len(out), grepMaxOutputBytes+2048)
	assert.Contains(t, out, "(truncated at")
}
