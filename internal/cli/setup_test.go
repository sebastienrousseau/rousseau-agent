package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for `rousseau setup` — the first-run wizard.
// The wizard is the operator's first interaction with the binary,
// so bugs here are the highest-visibility class in the product.

// -- defaultConfigPath ----------------------------------------------

func TestDefaultConfigPath_UsesXDGWhenSet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test-home")
	got, err := defaultConfigPath()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/xdg-test-home/rousseau/config.yaml", got)
}

func TestDefaultConfigPath_FallsBackToHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := defaultConfigPath()
	require.NoError(t, err)
	assert.Contains(t, got, ".config/rousseau/config.yaml",
		"empty XDG_CONFIG_HOME must fall back to $HOME/.config/rousseau/config.yaml")
}

// -- renderSetupYAML ------------------------------------------------

func TestRenderSetupYAML_ClaudeCLIMinimal(t *testing.T) {
	got := renderSetupYAML(setupAnswers{Provider: "claudecli", Transport: "cli"})
	assert.Contains(t, got, "provider: claudecli")
	assert.Contains(t, got, "state:\n  driver: sqlite")
	assert.NotContains(t, got, "anthropic:", "claudecli provider must not emit anthropic block")
	assert.NotContains(t, got, "api_key")
	assert.NotContains(t, got, "ROUSSEAU_WHATSAPP_ALLOW",
		"cli-only transport must not emit WhatsApp env-var hint")
}

func TestRenderSetupYAML_AnthropicWithKey(t *testing.T) {
	got := renderSetupYAML(setupAnswers{Provider: "anthropic", AnthropicKey: "sk-ant-example"})
	assert.Contains(t, got, "provider: anthropic")
	assert.Contains(t, got, "anthropic:")
	assert.Contains(t, got, "api_key: sk-ant-example")
}

func TestRenderSetupYAML_AnthropicWithoutKeyEmitsHint(t *testing.T) {
	got := renderSetupYAML(setupAnswers{Provider: "anthropic"})
	assert.Contains(t, got, "provider: anthropic")
	assert.Contains(t, got, "# api_key:", "empty key must emit a commented-out example line")
	assert.Contains(t, got, "ANTHROPIC_API_KEY")
}

func TestRenderSetupYAML_OpenAIIncludesModel(t *testing.T) {
	got := renderSetupYAML(setupAnswers{Provider: "openai", OpenAIKey: "sk-example"})
	assert.Contains(t, got, "provider: openai")
	assert.Contains(t, got, "api_key: sk-example")
	assert.Contains(t, got, "model: gpt-5")
}

func TestRenderSetupYAML_OllamaBaseURLPreserved(t *testing.T) {
	got := renderSetupYAML(setupAnswers{Provider: "ollama", OllamaURL: "http://192.168.1.5:11434/v1"})
	assert.Contains(t, got, "provider: ollama")
	assert.Contains(t, got, "base_url: http://192.168.1.5:11434/v1")
	assert.Contains(t, got, "model: llama3.1:8b")
}

func TestRenderSetupYAML_WhatsAppJIDInComment(t *testing.T) {
	got := renderSetupYAML(setupAnswers{Provider: "claudecli", Transport: "whatsapp", WhatsAppJID: "15551234567@s.whatsapp.net"})
	assert.Contains(t, got, "ROUSSEAU_WHATSAPP_ALLOW=15551234567@s.whatsapp.net",
		"the operator's own JID must appear verbatim in the recommendation")
}

func TestRenderSetupYAML_WhatsAppNoJIDPlaceholder(t *testing.T) {
	got := renderSetupYAML(setupAnswers{Provider: "claudecli", Transport: "whatsapp"})
	assert.Contains(t, got, "ROUSSEAU_WHATSAPP_ALLOW=<your JID>",
		"missing JID must fall back to a placeholder + literal warning")
}

// -- checkOverwrite -------------------------------------------------

func TestCheckOverwrite_NewFilePasses(t *testing.T) {
	w := setupWizard{
		configPath: filepath.Join(t.TempDir(), "config.yaml"),
	}
	require.NoError(t, w.checkOverwrite())
}

func TestCheckOverwrite_NonInteractiveExistingFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("existing"), 0o600))

	w := setupWizard{
		configPath:     path,
		nonInteractive: true,
		out:            &bytes.Buffer{},
	}
	err := w.checkOverwrite()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "--yes mode")
}

func TestCheckOverwrite_InteractiveYesConfirms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("existing"), 0o600))

	w := setupWizard{
		configPath: path,
		in:         strings.NewReader("yes\n"),
		out:        &bytes.Buffer{},
	}
	assert.NoError(t, w.checkOverwrite())
}

func TestCheckOverwrite_InteractiveOtherCancels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("existing"), 0o600))

	w := setupWizard{
		configPath: path,
		in:         strings.NewReader("no\n"),
		out:        &bytes.Buffer{},
	}
	err := w.checkOverwrite()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
}

// -- writeConfig ----------------------------------------------------

func TestWriteConfig_CreatesFileAtMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "config.yaml")
	w := setupWizard{configPath: path, out: &bytes.Buffer{}}
	require.NoError(t, w.writeConfig(setupAnswers{Provider: "claudecli", Transport: "cli"}))

	fi, err := os.Stat(path)
	require.NoError(t, err)
	// File mode may be affected by umask; assert on the owner
	// bits alone (0o600 → owner rw, others zero).
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(),
		"config file must be 0600 because it may embed API keys")

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(body), "provider: claudecli")
}

// -- End-to-end run --------------------------------------------------

// TestRun_NonInteractiveDefaultsWritesClaudeCLIWhatsApp verifies
// the full wizard composition under --yes: no prompts read, but
// the config file gets written with the documented defaults
// (claudecli + whatsapp).
func TestRun_NonInteractiveDefaultsWritesClaudeCLIWhatsApp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	var out bytes.Buffer
	w := setupWizard{
		in:             strings.NewReader(""),
		out:            &out,
		configPath:     path,
		nonInteractive: true,
	}
	require.NoError(t, w.run())

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(body), "provider: claudecli")
	assert.Contains(t, string(body), "ROUSSEAU_WHATSAPP_ALLOW",
		"whatsapp default transport must have hint in the config")

	// Next-steps text mentions the correct commands.
	assert.Contains(t, out.String(), "rousseau doctor")
	assert.Contains(t, out.String(), "rousseau whatsapp")
}

func TestRun_ExistingFileRefusedInNonInteractive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("prior"), 0o600))

	w := setupWizard{
		in:             strings.NewReader(""),
		out:            &bytes.Buffer{},
		configPath:     path,
		nonInteractive: true,
	}
	require.Error(t, w.run(), "existing config in --yes mode must refuse")
	// Original content still there.
	body, _ := os.ReadFile(path)
	assert.Equal(t, "prior", string(body))
}

// -- readLine -------------------------------------------------------

func TestReadLine_TrimsCRLF(t *testing.T) {
	got, err := readLine(strings.NewReader("hello\r\n"))
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
}

func TestReadLine_HandlesNoNewline(t *testing.T) {
	got, err := readLine(strings.NewReader("hello"))
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
}

// -- Command wiring -------------------------------------------------

func TestNewSetupCmd_Registered(t *testing.T) {
	root := NewRoot(&Options{})
	var found bool
	for _, sub := range root.Commands() {
		if sub.Name() == "setup" {
			found = true
			break
		}
	}
	assert.True(t, found, "NewRoot must register the setup subcommand")
}

func TestNewSetupCmd_HasExpectedFlags(t *testing.T) {
	cmd := newSetupCmd(&Options{})
	assert.NotNil(t, cmd.Flags().Lookup("yes"))
	assert.NotNil(t, cmd.Flags().Lookup("config"))
}
