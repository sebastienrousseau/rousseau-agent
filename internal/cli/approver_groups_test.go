package cli

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
)

// TestWrapWithMultiParty_CarriesApproverGroups (L-24): the
// approver_groups config key reaches the pending request voters see.
func TestWrapWithMultiParty_CarriesApproverGroups(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got, pending := wrapWithMultiParty(agent.AllowAllApprover{},
		config.MultiPartyConfig{Rules: []config.MultiPartyRule{
			{Tool: "bash", NeededApprovals: 1, Timeout: 2 * time.Second, ApproverGroups: []string{"sre"}},
		}}, chk, nil, silentLogger())
	require.NotNil(t, pending)

	ctx, cancel := context.WithCancel(sso.WithIdentity(context.Background(), sso.Identity{Subject: "okta|alice"}))
	defer cancel()
	go got.Approve(ctx, agent.ApprovalRequest{ToolName: "bash"}) // resolved by cancel
	require.Eventually(t, func() bool { return len(pending.List()) == 1 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"sre"}, pending.List()[0].ApproverGroups)
}
