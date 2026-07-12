package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestMask_ShortReturnsStars(t *testing.T) {
	assert.Equal(t, "***", mask("abc"))
}

func TestMask_LongReturnsPrefixSuffix(t *testing.T) {
	got := mask("sk-ant-abcdefghij1234")
	assert.True(t, strings.HasPrefix(got, "sk-a"))
	assert.True(t, strings.HasSuffix(got, "1234"))
}

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "0 B", humanBytes(0))
	assert.Equal(t, "512 B", humanBytes(512))
	assert.Equal(t, "1.00 KiB", humanBytes(1024))
	assert.Equal(t, "1.00 MiB", humanBytes(1024*1024))
	assert.Equal(t, "1.00 GiB", humanBytes(1024*1024*1024))
	assert.Equal(t, "2.50 KiB", humanBytes(2560))
}

func TestHasFailures(t *testing.T) {
	assert.False(t, hasFailures(nil))
	assert.False(t, hasFailures([]diagResult{{Status: "ok"}, {Status: "warn"}}))
	assert.True(t, hasFailures([]diagResult{{Status: "ok"}, {Status: "fail"}}))
}

func TestRenderReport_AllStatuses(t *testing.T) {
	buf := &bytes.Buffer{}
	renderReport(buf, []diagResult{
		{Name: "ok-thing", Status: "ok", Detail: "good"},
		{Name: "warn-thing", Status: "warn", Detail: "iffy"},
		{Name: "fail-thing", Status: "fail", Detail: "broken"},
		{Name: "info-thing", Status: "info", Detail: "note"},
		{Name: "unknown-thing", Status: "mystery", Detail: "?"},
	})
	out := buf.String()
	assert.Contains(t, out, "✔")
	assert.Contains(t, out, "!")
	assert.Contains(t, out, "✘")
	assert.Contains(t, out, "·")
	assert.Contains(t, out, "?")
}

func TestVersionOf_HappyPath(t *testing.T) {
	// GNU coreutils' true --version prints a banner; BusyBox's does not.
	// Either way the exit code is 0 and versionOf must not error.
	_, err := versionOf(context.Background(), "true")
	require.NoError(t, err)
}

func TestVersionOf_MissingBinary(t *testing.T) {
	_, err := versionOf(context.Background(), "/nonexistent/binary")
	assert.Error(t, err)
}

func TestCountSessions_NonExistentPath(t *testing.T) {
	_, err := countSessions("/nonexistent/path/db.sqlite")
	assert.Error(t, err)
}

func TestCheckBuild(t *testing.T) {
	got := checkBuild()
	require.Len(t, got, 2)
	assert.Equal(t, "build.version", got[0].Name)
	assert.Equal(t, "build.go", got[1].Name)
	assert.Equal(t, "info", got[0].Status)
}

func TestCheckProvider_ClaudeCLIMissingBinary(t *testing.T) {
	cfg := &config.Config{
		Provider:  "claudecli",
		ClaudeCLI: config.ClaudeCLIConfig{Binary: "/definitely/not/on/path"},
	}
	got := checkProvider(context.Background(), cfg)
	// First entry: provider.selected (info)
	assert.Equal(t, "provider.selected", got[0].Name)
	// Second: missing binary → fail
	require.Len(t, got, 2)
	assert.Equal(t, "fail", got[1].Status)
}

func TestCheckProvider_ClaudeCLIFound(t *testing.T) {
	cfg := &config.Config{
		Provider:  "claudecli",
		ClaudeCLI: config.ClaudeCLIConfig{Binary: "true", PermissionMode: "acceptEdits"},
	}
	got := checkProvider(context.Background(), cfg)
	// Look for the ok binary status.
	var found bool
	for _, r := range got {
		if r.Name == "provider.claudecli.binary" && r.Status == "ok" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestCheckProvider_AnthropicMissingKey(t *testing.T) {
	cfg := &config.Config{Provider: "anthropic"}
	got := checkProvider(context.Background(), cfg)
	var haveFail bool
	for _, r := range got {
		if r.Status == "fail" {
			haveFail = true
		}
	}
	assert.True(t, haveFail)
}

func TestCheckProvider_AnthropicWithKey(t *testing.T) {
	cfg := &config.Config{
		Provider:  "anthropic",
		Anthropic: config.AnthropicConfig{APIKey: "sk-ant-1234abcd5678efgh"},
	}
	got := checkProvider(context.Background(), cfg)
	var haveOK bool
	for _, r := range got {
		if r.Name == "provider.anthropic.api_key" && r.Status == "ok" {
			haveOK = true
		}
	}
	assert.True(t, haveOK)
}

func TestCheckProvider_UnknownProvider(t *testing.T) {
	cfg := &config.Config{Provider: "gemini"}
	got := checkProvider(context.Background(), cfg)
	var haveFail bool
	for _, r := range got {
		if r.Status == "fail" {
			haveFail = true
		}
	}
	assert.True(t, haveFail)
}

func TestCheckState_NonExistentIsInfo(t *testing.T) {
	cfg := &config.Config{State: config.StateConfig{Path: filepath.Join(t.TempDir(), "sessions.db")}}
	got := checkState(cfg)
	// db_size should be info (does not exist yet).
	var haveInfo bool
	for _, r := range got {
		if r.Name == "state.db_size" && r.Status == "info" {
			haveInfo = true
		}
	}
	assert.True(t, haveInfo)
}

func TestCheckState_ExistingFileIsOK(t *testing.T) {
	// Point at any existing file; the size check only needs a stat.
	path := filepath.Join(t.TempDir(), "sessions.db")
	require.NoError(t, os.WriteFile(path, []byte("dummy"), 0o600))
	cfg := &config.Config{State: config.StateConfig{Path: path}}
	got := checkState(cfg)
	var haveSize bool
	for _, r := range got {
		if r.Name == "state.db_size" {
			haveSize = true
		}
	}
	assert.True(t, haveSize)
}

func TestCheckWhatsApp_VoiceEnabledMissingBinary(t *testing.T) {
	cfg := &config.Config{
		WhatsApp: config.WhatsAppConfig{
			Voice: config.VoiceConfig{Enabled: true, Binary: "/not/on/path/whisper"},
		},
	}
	got := checkWhatsApp(cfg)
	var haveFail bool
	for _, r := range got {
		if r.Name == "whatsapp.voice.binary" && r.Status == "fail" {
			haveFail = true
		}
	}
	assert.True(t, haveFail)
}

func TestCheckWhatsApp_VoiceDisabled(t *testing.T) {
	cfg := &config.Config{}
	got := checkWhatsApp(cfg)
	var haveInfo bool
	for _, r := range got {
		if r.Name == "whatsapp.voice" && r.Status == "info" {
			haveInfo = true
		}
	}
	assert.True(t, haveInfo)
}

func TestCheckConfig(t *testing.T) {
	cfg := &config.Config{
		Log:      config.LogConfig{Level: "info", Format: "text"},
		Agent:    config.AgentConfig{MaxIterations: 16},
		WhatsApp: config.WhatsAppConfig{ReplyHeader: "hi\n\n"},
	}
	got := checkConfig(cfg)
	assert.NotEmpty(t, got)
}

func TestRunChecks_ComposesAllSections(t *testing.T) {
	cfg := &config.Config{Provider: "claudecli", ClaudeCLI: config.ClaudeCLIConfig{Binary: "true"}}
	got := runChecks(context.Background(), cfg)
	assert.NotEmpty(t, got)
}
