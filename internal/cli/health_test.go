package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/health"
)

// TestHealthCmd pins the container HealthCmd contract end to end
// through the root command: healthy only with a fresh, connected beat,
// and independent of config (a broken config must not mask a dead
// daemon, nor fail a healthy one).
func TestHealthCmd(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	badCfg := filepath.Join(t.TempDir(), "broken.yaml")
	require.NoError(t, os.WriteFile(badCfg, []byte("::: not yaml"), 0o600))

	run := func() error {
		root := NewRoot(&Options{})
		root.SetArgs([]string{"--config", badCfg, "health", "whatsapp"})
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		return root.ExecuteContext(context.Background())
	}

	assert.ErrorContains(t, run(), "no heartbeat")

	p := health.Path(filepath.Join(data, "rousseau"), "whatsapp")
	up, down := true, false
	require.NoError(t, health.Write(p, health.Beat{Transport: "whatsapp", UpdatedAt: time.Now().UTC(), Connected: &up}))
	assert.NoError(t, run())

	require.NoError(t, health.Write(p, health.Beat{Transport: "whatsapp", UpdatedAt: time.Now().UTC(), Connected: &down}))
	assert.ErrorContains(t, run(), "not connected")
}
