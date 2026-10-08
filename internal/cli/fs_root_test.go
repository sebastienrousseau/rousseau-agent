package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestDaemonFSRoot(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	assert.Equal(t, filepath.Join(data, "rousseau", "workspace"), daemonFSRoot(config.FSConfig{}))
	assert.Equal(t, "/srv/ws", daemonFSRoot(config.FSConfig{Root: "/srv/ws"}))
	assert.Equal(t, "/", daemonFSRoot(config.FSConfig{Root: "/"}), "explicit opt-out")
}

// The daemon's file tools cannot reach a file outside the workspace
// unless the operator opts out with root "/".
func TestDaemonFileTools_ConfinedToWorkspaceByDefault(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	outside := filepath.Join(t.TempDir(), "notes.txt")
	require.NoError(t, os.WriteFile(outside, []byte("private"), 0o600))
	input, err := json.Marshal(map[string]string{"path": outside})
	require.NoError(t, err)

	opts := makeDaemonOpts(t)
	opts.Config.Tools.FS = config.FSConfig{}
	reg, err := buildDaemonToolRegistry(opts)
	require.NoError(t, err)
	read, ok := reg.Get("read")
	require.True(t, ok)
	_, err = read.Execute(context.Background(), input)
	assert.ErrorContains(t, err, "outside the workspace root")

	opts.Config.Tools.FS = config.FSConfig{Root: "/"}
	reg, err = buildDaemonToolRegistry(opts)
	require.NoError(t, err)
	read, _ = reg.Get("read")
	out, err := read.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, out, "private")
}
