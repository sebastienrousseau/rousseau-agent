package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// viper lower-cases map keys on read; environment variable names
// must reach the subprocess exactly as written.
func TestLoad_PreservesEnvKeyCase(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	p := writeConfig(t, `
mcp:
  clients:
    github:
      command: gh-mcp
      env:
        GITHUB_PERSONAL_ACCESS_TOKEN: tok
        LowerCamel: v
hooks:
  pre_tool_use:
    - name: audit
      command: /bin/true
      env:
        AUDIT_SINK_URL: http://x
`)
	cfg, err := Load(p)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "tok", "LowerCamel": "v"}, cfg.MCP.Clients["github"].Env)
	require.Len(t, cfg.Hooks.PreToolUse, 1)
	assert.Equal(t, map[string]string{"AUDIT_SINK_URL": "http://x"}, cfg.Hooks.PreToolUse[0].Env)
}

func TestRestoreEnvKeyCase_NoFileIsNoop(t *testing.T) {
	cfg := &Config{}
	assert.NoError(t, restoreEnvKeyCase(cfg, ""))
	assert.NoError(t, restoreEnvKeyCase(cfg, "/nonexistent/config.yaml"))
}
