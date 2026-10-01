package cli

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/claudecli"
)

func TestWrapWithRisk(t *testing.T) {
	inner := agent.AllowAllApprover{}
	prov := claudecli.New(claudecli.Config{Model: "big"})

	got := wrapWithRisk(inner, config.RiskConfig{}, prov, config.ClaudeCLIConfig{}, license.Core(), silentLogger())
	assert.Equal(t, inner, got, "off by default")

	got = wrapWithRisk(inner, config.RiskConfig{Enabled: true, Tools: []string{"bash"}}, prov,
		config.ClaudeCLIConfig{}, license.Core(), silentLogger())
	d, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "Bash"})
	assert.Equal(t, agent.DecisionDeny, d, "unlicensed fails closed on scored tools")
	d, _ = got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	assert.Equal(t, agent.DecisionAllow, d)

	chk := signAndLoadLicense(t, license.Claims{
		Subject: "cust-gov", Tier: license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got = wrapWithRisk(inner, config.RiskConfig{Enabled: true, Tools: []string{"bash"}, Model: "haiku"}, prov,
		config.ClaudeCLIConfig{Binary: "claude"}, chk, silentLogger())
	ra, ok := got.(*agent.RiskApprover)
	require.True(t, ok)
	assert.InDelta(t, 0.8, ra.Threshold, 1e-9, "default threshold")
	assert.True(t, ra.FailClosed, "fails closed unless fail_open")
	assert.Equal(t, 60*time.Second, ra.Timeout)
	assert.NotSame(t, prov, ra.Provider, "a judge model override gets its own provider")
}
