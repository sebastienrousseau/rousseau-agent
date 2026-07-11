package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", cfg.Anthropic.Model)
	assert.Equal(t, int64(4096), cfg.Anthropic.MaxTokens)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "text", cfg.Log.Format)
	assert.Equal(t, 32, cfg.Agent.MaxIterations)
}

func TestLoad_YAMLFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
anthropic:
  api_key: from-file
  model: claude-opus-4-7
  max_tokens: 8192
log:
  level: debug
  format: json
agent:
  max_iterations: 12
  system_prompt: "custom"
state:
  path: /tmp/rousseau.db
`), 0o600))

	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "from-file", cfg.Anthropic.APIKey)
	assert.Equal(t, "claude-opus-4-7", cfg.Anthropic.Model)
	assert.Equal(t, int64(8192), cfg.Anthropic.MaxTokens)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, 12, cfg.Agent.MaxIterations)
	assert.Equal(t, "custom", cfg.Agent.SystemPrompt)
	assert.Equal(t, "/tmp/rousseau.db", cfg.State.Path)
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "from-env")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "from-env", cfg.Anthropic.APIKey)
}

func TestLoad_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(":\n  bad indentation:\nthis:is:not:yaml"), 0o600))

	_, err := Load(path)
	assert.Error(t, err)
}

func TestLoad_EmptyPathDefaultsHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load("")
	require.NoError(t, err)
	assert.NotNil(t, cfg)
}
