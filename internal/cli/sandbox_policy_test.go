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
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/fsguard"
)

func TestRequireSandboxPolicy(t *testing.T) {
	cases := []struct {
		name    string
		cfg     config.BashConfig
		wantErr bool
	}{
		{"unset kind, no opt-in", config.BashConfig{}, true},
		{"explicit none, no opt-in", config.BashConfig{Sandbox: config.BashSandboxConfig{Kind: "none"}}, true},
		{"none with opt-in", config.BashConfig{Sandbox: config.BashSandboxConfig{Kind: "none", AllowUnsandboxed: true}}, false},
		{"unset with opt-in", config.BashConfig{Sandbox: config.BashSandboxConfig{AllowUnsandboxed: true}}, false},
		{"nsjail", config.BashConfig{Sandbox: config.BashSandboxConfig{Kind: "nsjail"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireSandboxPolicy(tc.cfg, "slack")
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "allow_unsandboxed")
				assert.Contains(t, err.Error(), "slack")
				return
			}
			assert.NoError(t, err)
		})
	}
}

// The daemon refuses to assemble with an unsandboxed bash tool and no
// opt-in; this is the regression guard for the default-deny posture.
func TestAssembleDaemon_RefusesUnsandboxedBashWithoutOptIn(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Tools = config.ToolsConfig{}
	_, err := assembleDaemon(context.Background(), opts, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tools.bash.sandbox.kind")
}

func TestBuildFSGuard_RejectsRelativeRoot(t *testing.T) {
	_, err := buildFSGuard(config.FSConfig{Root: "relative"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tools.fs")
}

// The guard built from tools.fs reaches every file tool: a read of
// the daemon's own config dir is refused even with no root set, and a
// workspace root confines writes.
func TestRegisterFileTools_ShareOneGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	root := t.TempDir()
	g, err := buildFSGuard(config.FSConfig{Root: root})
	require.NoError(t, err)

	reg := tools.NewRegistry()
	registerFileTools(reg, g)
	for _, name := range []string{"read", "write", "edit", "grep"} {
		_, ok := reg.Get(name)
		assert.True(t, ok, name)
	}

	cfgPath := filepath.Join(home, ".config", "rousseau", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
	require.NoError(t, os.WriteFile(cfgPath, []byte("api_key: x"), 0o600))

	read, _ := reg.Get("read")
	_, err = read.Execute(context.Background(), toolInput(t, map[string]string{"path": cfgPath}))
	assert.ErrorIs(t, err, fsguard.ErrDenied, "deny list wins before the root check")

	write, _ := reg.Get("write")
	_, err = write.Execute(context.Background(), toolInput(t, map[string]string{"path": filepath.Join(home, "escape.txt"), "content": "x"}))
	assert.ErrorIs(t, err, fsguard.ErrOutsideRoot)

	_, err = write.Execute(context.Background(), toolInput(t, map[string]string{"path": filepath.Join(root, "ok.txt"), "content": "x"}))
	assert.NoError(t, err)
}

func toolInput(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
