package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// TestRequirePermissionMode pins that an unattended claudecli daemon
// never picks bypassPermissions on the operator's behalf: the mode
// must be chosen explicitly, and the error says how.
func TestRequirePermissionMode(t *testing.T) {
	opts := &Options{Config: &config.Config{Provider: "claudecli"}, Logger: silentLogger()}
	err := requirePermissionMode(opts, "whatsapp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claudecli.permission_mode")
	assert.Contains(t, err.Error(), "ROUSSEAU_CLAUDECLI_PERMISSION_MODE")
	assert.Empty(t, opts.Config.ClaudeCLI.PermissionMode, "must not be filled in silently")

	opts.Config.ClaudeCLI.PermissionMode = "bypassPermissions"
	assert.NoError(t, requirePermissionMode(opts, "whatsapp"))

	// Unset provider means claudecli (the default) and is checked too.
	assert.Error(t, requirePermissionMode(&Options{Config: &config.Config{}, Logger: silentLogger()}, "slack"))
	// Other providers run the rousseau approver natively.
	assert.NoError(t, requirePermissionMode(&Options{Config: &config.Config{Provider: "anthropic"}, Logger: silentLogger()}, "slack"))
}

// TestRequireSenderPolicy pins fail-closed inbound: an empty allowlist
// is refused unless the operator opted into --allow-anyone.
func TestRequireSenderPolicy(t *testing.T) {
	err := requireSenderPolicy("whatsapp", nil, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--allow-anyone")
	assert.NoError(t, requireSenderPolicy("whatsapp", []string{"1@s.whatsapp.net"}, false))
	assert.NoError(t, requireSenderPolicy("whatsapp", nil, true))
}

func TestNormalizeEmailAllowlist(t *testing.T) {
	assert.Equal(t, []string{"alice@example.com", "bob@example.org"},
		normalizeEmailAllowlist([]string{" Alice@Example.COM ", "", "bob@example.org"}))
}

func TestLoad_PermissionModeFromEnv(t *testing.T) {
	t.Setenv("ROUSSEAU_CLAUDECLI_PERMISSION_MODE", "dontAsk")
	cfg, err := config.Load(t.TempDir() + "/missing.yaml")
	require.NoError(t, err)
	assert.Equal(t, "dontAsk", cfg.ClaudeCLI.PermissionMode)
}
