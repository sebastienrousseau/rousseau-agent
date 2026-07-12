package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func makeDaemonOpts(t *testing.T) *Options {
	t.Helper()
	return &Options{
		Config: &config.Config{
			State: config.StateConfig{Path: filepath.Join(t.TempDir(), "sessions.db")},
		},
		Logger: silentLogger(),
	}
}

func TestSetUnattendedPermissionDefault_SetsBypassForClaudeCLI(t *testing.T) {
	opts := &Options{
		Config: &config.Config{Provider: "claudecli"},
		Logger: silentLogger(),
	}
	setUnattendedPermissionDefault(opts, "test")
	assert.Equal(t, "bypassPermissions", opts.Config.ClaudeCLI.PermissionMode)
}

func TestSetUnattendedPermissionDefault_DefaultProvider(t *testing.T) {
	// Empty provider is treated as claudecli — must still set bypass.
	opts := &Options{Config: &config.Config{}, Logger: silentLogger()}
	setUnattendedPermissionDefault(opts, "signal")
	assert.Equal(t, "bypassPermissions", opts.Config.ClaudeCLI.PermissionMode)
}

func TestSetUnattendedPermissionDefault_LeavesExplicitValue(t *testing.T) {
	opts := &Options{
		Config: &config.Config{
			Provider:  "claudecli",
			ClaudeCLI: config.ClaudeCLIConfig{PermissionMode: "acceptEdits"},
		},
		Logger: silentLogger(),
	}
	setUnattendedPermissionDefault(opts, "whatsapp")
	assert.Equal(t, "acceptEdits", opts.Config.ClaudeCLI.PermissionMode)
}

func TestSetUnattendedPermissionDefault_LeavesNonClaudeCLI(t *testing.T) {
	opts := &Options{
		Config: &config.Config{Provider: "anthropic"},
		Logger: silentLogger(),
	}
	setUnattendedPermissionDefault(opts, "signal")
	assert.Empty(t, opts.Config.ClaudeCLI.PermissionMode)
}

func TestAssembleDaemon_WhenProviderBuildFails(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "unknown"
	_, err := assembleDaemon(context.Background(), opts, nil)
	assert.Error(t, err)
}

func TestAssembleDaemon_HappyPath(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}

	wiring, err := assembleDaemon(context.Background(), opts, []string{"1@s.whatsapp.net"})
	require.NoError(t, err)
	defer func() { _ = wiring.Sessions.Close() }() //nolint:errcheck // test cleanup

	assert.NotNil(t, wiring.Provider)
	assert.NotNil(t, wiring.Agent)
	assert.NotNil(t, wiring.Router)
	assert.NotNil(t, wiring.CronStore)
	assert.NotNil(t, wiring.Sessions)
	assert.NotNil(t, wiring.JIDMap)
	assert.NotNil(t, wiring.ClaudeCache)
}

func TestStartCron_StartsAndShutsDownCleanly(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}

	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Sessions.Close() }() //nolint:errcheck // test cleanup

	delivery := func(context.Context, string, string) error { return nil }
	shutdown, err := wiring.startCron(context.Background(), delivery, silentLogger())
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	assert.NotPanics(t, func() { shutdown() })
}
