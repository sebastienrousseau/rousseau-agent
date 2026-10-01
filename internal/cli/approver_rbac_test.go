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

func TestWrapWithRBAC_UnlicensedDeniesGovernedTools(t *testing.T) {
	// Rules configured, no licence: the rules cannot run, so the
	// tools they govern fail closed (used to fall through to the
	// inner allow-all, i.e. ungoverned). Other tools still go to inner.
	inner := agent.AllowAllApprover{}
	cfg := config.RBACConfig{Rules: []config.RBACRule{
		{Tool: "bash", AllowedGroups: []string{"eng"}},
	}}
	got := wrapWithRBAC(inner, cfg, license.Core(), silentLogger())

	decision, reason := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "Bash"})
	assert.Equal(t, agent.DecisionDeny, decision, "governed tool fails closed")
	assert.Contains(t, reason, "not licensed")
	decision, _ = got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	assert.Equal(t, agent.DecisionAllow, decision, "ungoverned tools still reach inner")
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
	// Defensive: an upstream wiring bug passing nil must behave
	// like no licence, i.e. fail closed on the governed tools.
	cfg := config.RBACConfig{Rules: []config.RBACRule{
		{Tool: "bash", AllowedGroups: []string{"eng"}},
	}}
	got := wrapWithRBAC(agent.AllowAllApprover{}, cfg, nil, silentLogger())
	decision, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
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
