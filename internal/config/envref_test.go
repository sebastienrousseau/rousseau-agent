package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

// The README documents `api_key: ${ANTHROPIC_API_KEY}`; the literal
// string used to reach the provider unexpanded.
func TestLoad_ExpandsEnvRefsInStringsSlicesAndMaps(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("PROBE_KEY", "sk-live")
	t.Setenv("PROBE_TOKEN", "ghp-1")
	t.Setenv("PROBE_DIR", "/srv/secrets")
	p := writeConfig(t, `
provider: anthropic
anthropic:
  api_key: "${PROBE_KEY}"
tools:
  fs:
    deny: ["${PROBE_DIR}/vault", "/static"]
mcp:
  clients:
    github:
      command: gh-mcp
      env:
        GITHUB_PERSONAL_ACCESS_TOKEN: "${PROBE_TOKEN}"
`)
	cfg, err := Load(p)
	require.NoError(t, err)
	assert.Equal(t, "sk-live", cfg.Anthropic.APIKey)
	assert.Equal(t, []string{"/srv/secrets/vault", "/static"}, cfg.Tools.FS.Deny)
	// viper lower-cases map keys on decode, so look the value up
	// case-insensitively; the expansion is what is under test here.
	var got string
	for k, v := range cfg.MCP.Clients["github"].Env {
		if strings.EqualFold(k, "GITHUB_PERSONAL_ACCESS_TOKEN") {
			got = v
		}
	}
	assert.Equal(t, "ghp-1", got)
}

// loadWithoutFile loads defaults the way a fresh install does: no
// explicit path, and no file at the default location.
func loadWithoutFile(t *testing.T) *Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load("")
	require.NoError(t, err)
	return cfg
}

func TestLoad_UnsetEnvRefIsAnError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	os.Unsetenv("ROUSSEAU_TEST_NOPE") //nolint:errcheck // ensure absent
	p := writeConfig(t, "anthropic:\n  api_key: \"${ROUSSEAU_TEST_NOPE}\"\n")
	_, err := Load(p)
	require.ErrorIs(t, err, ErrUnsetEnvRef)
	assert.Contains(t, err.Error(), "ROUSSEAU_TEST_NOPE")
}

func TestLoad_BareDollarIsLeftAlone(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	p := writeConfig(t, "anthropic:\n  api_key: \"pa$$word$HOME\"\n")
	cfg, err := Load(p)
	require.NoError(t, err)
	assert.Equal(t, "pa$$word$HOME", cfg.Anthropic.APIKey)
}

// A typo in --config must not silently run the daemon on defaults.
func TestLoad_ExplicitMissingPathIsAnError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "typo.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
	assert.Contains(t, err.Error(), "--config")
}
