package cli

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stdin(lines ...string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

func TestRunInit_WritesClaudeCLIConfigByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	// Accept all defaults: enter for provider (claudecli), enter for
	// workspace, blank for whatsapp/telegram.
	require.NoError(t, runInit(out, stdin("", "", "", ""), &Options{}, true))

	cfg, err := os.ReadFile(filepath.Join(home, ".config", "rousseau", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfg), "provider: claudecli")
	assert.Contains(t, string(cfg), "permission_mode: bypassPermissions")
	assert.Contains(t, out.String(), "Next steps:")
}

func TestRunInit_AnthropicChoiceRecordsKeyAndModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	out := &bytes.Buffer{}
	err := runInit(out, stdin("2", "sk-ant-test", "claude-opus-4-7", "", "", ""), &Options{}, true)
	require.NoError(t, err)
	cfg, err := os.ReadFile(filepath.Join(home, ".config", "rousseau", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfg), "provider: anthropic")
	assert.Contains(t, string(cfg), "sk-ant-test")
	assert.Contains(t, string(cfg), "claude-opus-4-7")
}

func TestRunInit_BedrockChoiceRecordsRegionModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	out := &bytes.Buffer{}
	err := runInit(out, stdin("6", "us-east-1", "anthropic.claude-sonnet-4-6", "", "", ""), &Options{}, true)
	require.NoError(t, err)
	cfg, err := os.ReadFile(filepath.Join(home, ".config", "rousseau", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfg), "provider: bedrock")
	assert.Contains(t, string(cfg), "us-east-1")
}

func TestRunInit_ExistingConfigWithoutForceErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".config", "rousseau"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".config", "rousseau", "config.yaml"), []byte("x"), 0o600))

	out := &bytes.Buffer{}
	err := runInit(out, stdin("", "", "", ""), &Options{}, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestRunInit_TelegramWorkflowMentioned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	out := &bytes.Buffer{}
	err := runInit(out, stdin("1", "", "", "12345:bottoken"), &Options{}, true)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "rousseau telegram")
}

func TestPickProvider_UnknownFallsBackToClaudeCLI(t *testing.T) {
	name, _ := pickProvider("99", &bytes.Buffer{}, stdin(""))
	assert.Equal(t, "claudecli", name)
}

func TestClaudeBinary_HandlesMissingClaude(t *testing.T) {
	// LookPath returns "claude" fallback when the binary is missing;
	// with a scrubbed PATH we should still get a non-empty string back.
	t.Setenv("PATH", "")
	assert.NotEmpty(t, claudeBinary())
}
