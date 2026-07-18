package integrations_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/pkg/tools"
	"github.com/sebastienrousseau/rousseau-agent/pkg/tools/integrations"
)

func TestFacadeRegistersEnabledSuites(t *testing.T) {
	reg := tools.NewRegistry()
	cfg := integrations.Config{
		GitHub: integrations.GitHubConfig{Enabled: true, Token: "x"},
		Stripe: integrations.StripeConfig{Enabled: true, SecretKey: "sk_x"},
	}
	require.NoError(t, integrations.RegisterAll(reg, cfg, nil))
	// GitHub ships 9 tools, Stripe ships 2 → total 11.
	require.Len(t, reg.Names(), 11)
}
