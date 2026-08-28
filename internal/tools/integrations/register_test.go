package integrations

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func TestRegisterAll_AllSuitesDisabledYieldsEmptyRegistry(t *testing.T) {
	reg := tools.NewRegistry()
	require.NoError(t, RegisterAll(reg, Config{}, nil))
	assert.Empty(t, reg.Names())
}

func TestRegisterAll_AllEnabledRegistersEveryTool(t *testing.T) {
	reg := tools.NewRegistry()
	cfg := Config{
		GitHub: GitHubConfig{Enabled: true, Token: "gh"},
		Slack:  SlackConfig{Enabled: true, BotToken: "xoxb-t"},
		Google: GoogleConfig{Enabled: true, TokenFn: func(context.Context) (string, error) { return "at", nil }},
		Linear: LinearConfig{Enabled: true, APIKey: "lin_x"},
		Stripe: StripeConfig{Enabled: true, SecretKey: "sk_x"},
	}
	require.NoError(t, RegisterAll(reg, cfg, nil))
	names := strings.Join(reg.Names(), ",")
	// 9 github + 4 slack + 7 google + 4 linear + 2 stripe = 26
	assert.Len(t, reg.Names(), 26)
	for _, expected := range []string{
		"github_list_repos", "slack_post_message", "gmail_list",
		"calendar_list_events", "drive_search", "linear_list_issues",
		"stripe_list_charges",
	} {
		assert.Contains(t, names, expected)
	}
}

func TestRegisterAll_ErrorPropagates(t *testing.T) {
	reg := tools.NewRegistry()
	// GitHub enabled without token — should fail before touching the
	// other suites.
	t.Setenv("ROUSSEAU_GITHUB_TOKEN", "")
	cfg := Config{
		GitHub: GitHubConfig{Enabled: true},
		Slack:  SlackConfig{Enabled: true, BotToken: "xoxb-t"},
	}
	err := RegisterAll(reg, cfg, nil)
	assert.ErrorContains(t, err, "github")
	// Nothing should have registered (github failed early).
	assert.Empty(t, reg.Names())
}
