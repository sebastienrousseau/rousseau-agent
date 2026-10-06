package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestConfigValidate_TextAndJSON(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("provider: anthropic\ntelegram:\n  bot_token: t\n"), 0o600))
	opts := &Options{
		ConfigPath: cfgPath,
		Config: &config.Config{
			Provider: "anthropic",
			Telegram: config.TelegramConfig{Token: "t"},
			Tools:    config.ToolsConfig{FS: config.FSConfig{Root: "/srv/work"}},
		},
		Logger: silentLogger(),
	}

	cmd := newConfigCmd(opts)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"validate"})
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.Contains(t, out, cfgPath)
	assert.Contains(t, out, "provider:         anthropic")
	assert.Contains(t, out, "[telegram]")
	assert.Contains(t, out, "bash sandbox:     none")
	assert.Contains(t, out, "/srv/work")
	assert.Contains(t, out, "OK")

	buf.Reset()
	cmd = newConfigCmd(opts)
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"validate", "--json"})
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.Execute())
	var got configSummary
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, cfgPath, got.Path)
	assert.Equal(t, []string{"telegram"}, got.Transports)
	assert.Equal(t, "none", got.BashSandbox)
	assert.True(t, got.UnknownKeysOff)
}

func TestConfigValidate_NoConfigLoaded(t *testing.T) {
	cmd := newConfigCmd(&Options{Logger: silentLogger()})
	cmd.SilenceErrors = true
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"validate"})
	cmd.SetContext(context.Background())
	require.Error(t, cmd.Execute())
}

func TestResolvedConfigPath(t *testing.T) {
	assert.Equal(t, "/x/c.yaml", resolvedConfigPath("/x/c.yaml"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	assert.Contains(t, resolvedConfigPath(""), "absent")
	p := filepath.Join(home, ".config", "rousseau", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("provider: anthropic\n"), 0o600))
	assert.Equal(t, p, resolvedConfigPath(""))
}

func TestConfiguredTransports_Empty(t *testing.T) {
	assert.Equal(t, []string{}, configuredTransports(&config.Config{}))
}
