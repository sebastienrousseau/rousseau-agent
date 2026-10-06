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
			// An empty skills dir keeps assembleDaemon hermetic: the
			// default resolution would read $HOME/.local/share/rousseau/skills,
			// so a developer's own skills could fail the build step.
			Agent: config.AgentConfig{SkillsDir: t.TempDir()},
			// The daemon refuses an unsandboxed bash tool unless the
			// operator opts in; tests opt in so wiring stays hermetic.
			Tools: unsandboxedTools(),
		},
		Logger: silentLogger(),
	}
}

// unsandboxedTools is the explicit opt-in every daemon test fixture
// needs now that requireSandboxPolicy gates assembleDaemon.
func unsandboxedTools() config.ToolsConfig {
	return config.ToolsConfig{Bash: config.BashConfig{Sandbox: config.BashSandboxConfig{AllowUnsandboxed: true}}}
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
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup: closes everything assembleDaemon started

	assert.NotNil(t, wiring.Provider)
	assert.NotNil(t, wiring.Agent)
	assert.NotNil(t, wiring.Router)
	assert.NotNil(t, wiring.CronStore)
	assert.NotNil(t, wiring.Sessions)
	assert.NotNil(t, wiring.JIDMap)
	assert.NotNil(t, wiring.ClaudeCache)
	assert.NotNil(t, wiring.Identities, "Identities must be wired so /whoami, /link, /unlink work")
}

func TestTransportHandler_RouterCarriesIdentity(t *testing.T) {
	// Regression pin: /whoami, /link, /unlink used to fall through
	// to the LLM in every real deployment because assembleDaemon
	// built a Router without an Identity resolver — the resolver
	// exists in state, it just wasn't wired. routerFor now builds
	// per-transport routers that carry both Identity + Transport,
	// so identity-based chat commands actually answer.
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}

	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup: closes everything assembleDaemon started

	// TransportHandler triggers routerFor("whatsapp"). Repeated
	// calls must return the cached router, not build fresh ones.
	_ = wiring.TransportHandler("whatsapp", silentLogger())
	_ = wiring.TransportHandler("whatsapp", silentLogger())
	assert.Len(t, wiring.routers, 1, "routerFor must cache per-transport")

	r := wiring.routers["whatsapp"]
	require.NotNil(t, r)
}

func TestStartCron_StartsAndShutsDownCleanly(t *testing.T) {
	opts := makeDaemonOpts(t)
	opts.Config.Provider = "anthropic"
	opts.Config.Anthropic = config.AnthropicConfig{APIKey: "sk-test", Model: "claude"}

	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup: closes everything assembleDaemon started

	delivery := func(context.Context, string, string) error { return nil }
	shutdown, err := wiring.startCron(context.Background(), delivery, silentLogger())
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	assert.NotPanics(t, func() { shutdown() })
}
