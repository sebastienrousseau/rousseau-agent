package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/pkg/tools/integrations"
)

// clearEnv makes the suite-selection environment deterministic
// regardless of what the developer has exported.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"ROUSSEAU_GITHUB_TOKEN",
		"ROUSSEAU_SLACK_TOOLS_TOKEN",
		"ROUSSEAU_LINEAR_API_KEY",
		"ROUSSEAU_STRIPE_KEY",
	} {
		t.Setenv(k, "")
	}
}

func TestConfigFromEnvAllDisabled(t *testing.T) {
	clearEnv(t)

	cfg := configFromEnv()

	assert.False(t, cfg.GitHub.Enabled)
	assert.False(t, cfg.Slack.Enabled)
	assert.False(t, cfg.Linear.Enabled)
	assert.False(t, cfg.Stripe.Enabled)
}

func TestConfigFromEnvEnablesOnCredential(t *testing.T) {
	clearEnv(t)
	t.Setenv("ROUSSEAU_GITHUB_TOKEN", "ghp_example")
	t.Setenv("ROUSSEAU_STRIPE_KEY", "sk_test_example")

	cfg := configFromEnv()

	assert.True(t, cfg.GitHub.Enabled)
	assert.True(t, cfg.Stripe.Enabled)
	assert.False(t, cfg.Slack.Enabled)
	assert.False(t, cfg.Linear.Enabled)
}

func TestRunNoSuitesEnabled(t *testing.T) {
	clearEnv(t)
	var out, errOut bytes.Buffer

	code := run(&out, &errOut, configFromEnv())

	assert.Equal(t, 0, code)
	assert.Contains(t, out.String(), "registered 0 tool(s):")
	assert.Empty(t, errOut.String())
}

func TestRunRegistersEnabledSuite(t *testing.T) {
	clearEnv(t)
	t.Setenv("ROUSSEAU_GITHUB_TOKEN", "ghp_example")
	var out, errOut bytes.Buffer

	code := run(&out, &errOut, configFromEnv())

	assert.Equal(t, 0, code)
	assert.Contains(t, out.String(), "github_list_prs")
	assert.Empty(t, errOut.String())
}

func TestRunMissingCredentialIsFatal(t *testing.T) {
	clearEnv(t)
	var out, errOut bytes.Buffer

	// Enabled without a token anywhere: RegisterAll refuses to start.
	cfg := integrations.Config{GitHub: integrations.GitHubConfig{Enabled: true}}
	code := run(&out, &errOut, cfg)

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), "register:")
	assert.Contains(t, errOut.String(), "token required")
}
