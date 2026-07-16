package integrations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// TestRegisterAll_EachSuiteIndividually exercises every early-error
// branch: a single enabled suite with a missing credential is
// rejected before the daemon serves traffic.

func TestRegisterAll_SlackMissingTokenErrors(t *testing.T) {
	t.Setenv("ROUSSEAU_SLACK_TOOLS_TOKEN", "")
	reg := tools.NewRegistry()
	err := RegisterAll(reg, Config{Slack: SlackConfig{Enabled: true}}, nil)
	assert.ErrorContains(t, err, "slack")
}

func TestRegisterAll_GoogleWithoutTokenFnErrors(t *testing.T) {
	reg := tools.NewRegistry()
	err := RegisterAll(reg, Config{Google: GoogleConfig{Enabled: true}}, nil)
	assert.ErrorContains(t, err, "google")
}

func TestRegisterAll_LinearMissingKeyErrors(t *testing.T) {
	t.Setenv("ROUSSEAU_LINEAR_API_KEY", "")
	reg := tools.NewRegistry()
	err := RegisterAll(reg, Config{Linear: LinearConfig{Enabled: true}}, nil)
	assert.ErrorContains(t, err, "linear")
}

func TestRegisterAll_StripeMissingKeyErrors(t *testing.T) {
	t.Setenv("ROUSSEAU_STRIPE_KEY", "")
	reg := tools.NewRegistry()
	err := RegisterAll(reg, Config{Stripe: StripeConfig{Enabled: true}}, nil)
	assert.ErrorContains(t, err, "stripe")
}

func TestRegisterAll_ComposioMissingCredsErrors(t *testing.T) {
	t.Setenv("ROUSSEAU_COMPOSIO_API_KEY", "")
	t.Setenv("ROUSSEAU_COMPOSIO_USER_ID", "")
	reg := tools.NewRegistry()
	err := RegisterAll(reg, Config{Composio: ComposioConfig{Enabled: true}}, nil)
	assert.ErrorContains(t, err, "composio")
}

func TestRegisterAll_GitHubEnabledOnly(t *testing.T) {
	reg := tools.NewRegistry()
	err := RegisterAll(reg, Config{GitHub: GitHubConfig{Enabled: true, Token: "x"}}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, reg.Names())
}

func TestRegisterAll_StripeEnabledOnly(t *testing.T) {
	reg := tools.NewRegistry()
	err := RegisterAll(reg, Config{Stripe: StripeConfig{Enabled: true, SecretKey: "sk_x"}}, nil)
	require.NoError(t, err)
	assert.Len(t, reg.Names(), 2)
}

func TestRegisterAll_LinearEnabledOnly(t *testing.T) {
	reg := tools.NewRegistry()
	err := RegisterAll(reg, Config{Linear: LinearConfig{Enabled: true, APIKey: "lin_x"}}, nil)
	require.NoError(t, err)
	assert.Len(t, reg.Names(), 4)
}

func TestRegisterAll_GoogleWithTokenFn(t *testing.T) {
	reg := tools.NewRegistry()
	tokenFn := func(context.Context) (string, error) { return "at", nil }
	err := RegisterAll(reg, Config{Google: GoogleConfig{Enabled: true, TokenFn: tokenFn}}, nil)
	require.NoError(t, err)
	assert.Len(t, reg.Names(), 7)
}
