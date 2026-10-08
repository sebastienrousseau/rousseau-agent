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

// M-11: gvisor counts as a sandbox only while it confines the
// filesystem. A mount that is $HOME, an ancestor of it, or "/" puts
// ~/.ssh and the daemon config back in reach of bash, so the daemon
// refuses to start; allow_unsandboxed (an opt-in for kind none) does
// not cover a sandbox configured to expose the host.
func TestRequireSandboxPolicy_GVisorMustConfineFilesystem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := filepath.Join(home, ".local", "share", "rousseau", "workspace")
	gv := func(sb config.BashSandboxConfig) config.BashConfig {
		sb.Kind = "gvisor"
		return config.BashConfig{Sandbox: sb}
	}
	cases := []struct {
		name    string
		cfg     config.BashConfig
		wantErr bool
	}{
		{"default mounts", gv(config.BashSandboxConfig{}), false},
		{"workspace under home", gv(config.BashSandboxConfig{Writable: []string{ws}}), false},
		{"writable home", gv(config.BashSandboxConfig{Writable: []string{home}}), true},
		{"readonly parent of home", gv(config.BashSandboxConfig{Readonly: []string{filepath.Dir(home)}}), true},
		{"readonly slash", gv(config.BashSandboxConfig{Readonly: []string{"/"}}), true},
		{"opt-in does not cover exposure", gv(config.BashSandboxConfig{Writable: []string{home}, AllowUnsandboxed: true}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireSandboxPolicy(tc.cfg, "slack")
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "slack")
			assert.Contains(t, err.Error(), "gvisor")
			assert.Contains(t, err.Error(), "HOME")
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
	_, err := buildFSGuard(config.FSConfig{Root: "relative"}, config.AuditEgressConfig{})
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
	g, err := buildFSGuard(config.FSConfig{Root: root}, config.AuditEgressConfig{})
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
