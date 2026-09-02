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

func TestWrapWithRBAC_NoRulesReturnsInnerUnchanged(t *testing.T) {
	// Zero-config path: no rules means no wrap. Property: the
	// operator must not pay any RBAC overhead when they haven't
	// asked for it.
	inner := agent.DenyAllApprover{Reason: "inner"}
	got := wrapWithRBAC(inner, config.RBACConfig{}, license.Core(), silentLogger())
	assert.Equal(t, inner, got, "empty rules must return inner unchanged")
}

func TestWrapWithRBAC_UnlicensedFallsBackToInner(t *testing.T) {
	// The most common misconfiguration: operator writes rules
	// but hasn't attached a licence. Daemon must fall back to
	// the inner approver, not silently start denying. INFO log
	// is asserted elsewhere via the logger contract.
	inner := agent.AllowAllApprover{}
	cfg := config.RBACConfig{Rules: []config.RBACRule{
		{Tool: "bash", AllowedGroups: []string{"eng"}},
	}}
	got := wrapWithRBAC(inner, cfg, license.Core(), silentLogger())

	// If wrap DID take effect, an anonymous request to "bash"
	// would be denied. With the fallback, it must succeed.
	decision, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionAllow, decision,
		"unlicensed RBAC must fall through to inner allow-all, not start denying")
}

func TestWrapWithRBAC_LicensedRulesFilterAnonymous(t *testing.T) {
	// Full activation: licence unlocked + rules configured →
	// anonymous request to a covered tool must be denied.
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	cfg := config.RBACConfig{Rules: []config.RBACRule{
		{Tool: "bash", AllowedGroups: []string{"eng"}},
	}}
	got := wrapWithRBAC(agent.AllowAllApprover{}, cfg, chk, silentLogger())
	decision, reason := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "governance:")
}

func TestWrapWithRBAC_LicensedRulesLetGroupMemberThrough(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	cfg := config.RBACConfig{Rules: []config.RBACRule{
		{Tool: "bash", AllowedGroups: []string{"eng"}},
	}}
	got := wrapWithRBAC(agent.AllowAllApprover{}, cfg, chk, silentLogger())

	ctx := sso.WithIdentity(context.Background(), sso.Identity{
		Subject: "okta|alice", Groups: []string{"eng"},
	})
	decision, _ := got.Approve(ctx, agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

func TestWrapWithRBAC_BadRuleFallsBackToInner(t *testing.T) {
	// A rule with an empty tool name fails rbac.NewApprover.
	// The daemon must survive — inner returned as-is + WARN
	// logged.
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	cfg := config.RBACConfig{Rules: []config.RBACRule{
		{Tool: "", AllowedGroups: []string{"eng"}}, // invalid
	}}
	inner := agent.AllowAllApprover{}
	got := wrapWithRBAC(inner, cfg, chk, silentLogger())
	// If wrap succeeded, ApprovalRequest{} would hit the empty-
	// tool rule and fail. If it fell back, we get allow-all.
	decision, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	require.Equal(t, agent.DecisionAllow, decision)
}

func TestWrapWithRBAC_NilCheckerActsLikeUnlicensed(t *testing.T) {
	// Defensive: an upstream wiring bug passing nil must NOT
	// activate the gate silently.
	cfg := config.RBACConfig{Rules: []config.RBACRule{
		{Tool: "bash", AllowedGroups: []string{"eng"}},
	}}
	got := wrapWithRBAC(agent.AllowAllApprover{}, cfg, nil, silentLogger())
	decision, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

func TestCheckGovernance_UnconfiguredEmitsNothing(t *testing.T) {
	got := checkGovernance(&config.Config{}, license.Core())
	assert.Empty(t, got)
}

func TestCheckGovernance_UnlicensedWarns(t *testing.T) {
	got := checkGovernance(&config.Config{Agent: config.AgentConfig{
		Approver: config.ApproverConfig{RBAC: config.RBACConfig{Rules: []config.RBACRule{
			{Tool: "bash", AllowedGroups: []string{"eng"}},
		}}},
	}}, license.Core())

	var haveWarn bool
	for _, r := range got {
		if r.Status == "warn" {
			haveWarn = true
		}
	}
	assert.True(t, haveWarn)
}

func TestCheckGovernance_LicensedReportsOK(t *testing.T) {
	chk := signAndLoadLicense(t, license.Claims{
		Subject:   "cust-gov",
		Tier:      license.TierEnterprise,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(),
	})
	got := checkGovernance(&config.Config{Agent: config.AgentConfig{
		Approver: config.ApproverConfig{RBAC: config.RBACConfig{Rules: []config.RBACRule{
			{Tool: "bash", AllowedGroups: []string{"eng"}},
		}}},
	}}, chk)
	var status string
	for _, r := range got {
		if r.Name == "identity.governance.rbac.licensed" {
			status = r.Status
		}
	}
	assert.Equal(t, "ok", status)
}
