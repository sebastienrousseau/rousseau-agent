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

func TestWrapWithMultiParty_UnlicensedFallsBackToInner(t *testing.T) {
	// Most common misconfig: rules configured, no licence.
	// Daemon must fall back to inner — rules ignored, never
	// silently start blocking.
	inner := agent.AllowAllApprover{}
	got, pending := wrapWithMultiParty(inner,
		config.MultiPartyConfig{Rules: []config.MultiPartyRule{
			{Tool: "bash", NeededApprovals: 2},
		}}, license.Core(), nil, silentLogger())
	assert.Equal(t, inner, got)
	assert.Nil(t, pending, "unlicensed → nil manager so router intercept becomes inert")
}

func TestWrapWithMultiParty_BadRuleFallsBackToInner(t *testing.T) {
	// NeededApprovals=0 is caught by approval.NewApprover.
	// The wrap-layer must fail-safe: log WARN + return inner
	// unchanged (matches wrapWithRBAC pattern).
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got, pending := wrapWithMultiParty(agent.AllowAllApprover{},
		config.MultiPartyConfig{Rules: []config.MultiPartyRule{
			{Tool: "bash", NeededApprovals: 0}, // invalid
		}}, chk, nil, silentLogger())
	// wrap-skipped → inner returned + nil manager.
	assert.Equal(t, agent.AllowAllApprover{}, got)
	assert.Nil(t, pending)
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
