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
)

func TestWrapWithMultiParty_NoRulesReturnsInnerAndNilManager(t *testing.T) {
	// Zero-config path. Property: the manager is nil so the
	// router's approval-command intercept becomes inert; the
	// approver is unchanged.
	inner := agent.DenyAllApprover{Reason: "inner"}
	got, pending := wrapWithMultiParty(inner, config.MultiPartyConfig{}, license.Core(), nil, silentLogger())
	assert.Equal(t, inner, got)
	assert.Nil(t, pending)
}

func TestWrapWithMultiParty_UnlicensedDeniesGovernedTools(t *testing.T) {
	// Rules configured, no licence: the governed tool fails closed
	// (it used to run with no approvals); the router intercept stays
	// inert (nil manager).
	got, pending := wrapWithMultiParty(agent.AllowAllApprover{},
		config.MultiPartyConfig{Rules: []config.MultiPartyRule{
			{Tool: "bash", NeededApprovals: 2},
		}}, license.Core(), nil, silentLogger())
	assert.Nil(t, pending)
	decision, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
	decision, _ = got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

func TestWrapWithMultiParty_BadRuleDeniesGovernedTools(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got, pending := wrapWithMultiParty(agent.AllowAllApprover{},
		config.MultiPartyConfig{Rules: []config.MultiPartyRule{
			{Tool: "bash", NeededApprovals: 0}, // invalid
		}}, chk, nil, silentLogger())
	assert.Nil(t, pending)
	decision, reason := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "invalid")
}

func TestWrapWithMultiParty_LicensedReturnsWrappedApproverAndManager(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got, pending := wrapWithMultiParty(agent.AllowAllApprover{},
		config.MultiPartyConfig{Rules: []config.MultiPartyRule{
			{Tool: "bash", NeededApprovals: 2, Timeout: 100 * time.Millisecond},
		}}, chk, nil, silentLogger())
	require.NotNil(t, pending)
	// Wrapped approver must be a NEW value (not the raw
	// allow-all), verified by attempting an approval with no
	// SSO identity → the wrapper denies (anonymous requester).
	decision, reason := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "authenticated requester")
}

// -- checkGovernance covers multi_party rows --

func TestCheckGovernance_MultiPartyRowsSurfaceWhenConfigured(t *testing.T) {
	got := checkGovernance(&config.Config{Agent: config.AgentConfig{
		Approver: config.ApproverConfig{
			MultiParty: config.MultiPartyConfig{Rules: []config.MultiPartyRule{
				{Tool: "bash", NeededApprovals: 2},
			}},
		},
	}}, license.Core())

	var haveRulesRow, haveLicensedRow diagResult
	for _, r := range got {
		if r.Name == "identity.governance.multi_party.rules" {
			haveRulesRow = r
		}
		if r.Name == "identity.governance.multi_party.licensed" {
			haveLicensedRow = r
		}
	}
	assert.Contains(t, haveRulesRow.Detail, "1 rule(s) configured")
	assert.Equal(t, "warn", haveLicensedRow.Status,
		"unlicensed multi_party config must warn — matches the SSO / RBAC / OPA discipline")
}

func TestCheckGovernance_LicensedMultiPartyReportsOK(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got := checkGovernance(&config.Config{Agent: config.AgentConfig{
		Approver: config.ApproverConfig{
			MultiParty: config.MultiPartyConfig{Rules: []config.MultiPartyRule{
				{Tool: "bash", NeededApprovals: 2},
			}},
		},
	}}, chk)
	var status string
	for _, r := range got {
		if r.Name == "identity.governance.multi_party.licensed" {
			status = r.Status
		}
	}
	assert.Equal(t, "ok", status)
}
