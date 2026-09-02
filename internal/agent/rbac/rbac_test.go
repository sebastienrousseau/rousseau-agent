package rbac_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/rbac"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

func TestNewApprover_RejectsEmptyToolName(t *testing.T) {
	_, err := rbac.NewApprover([]rbac.Rule{{Tool: "", AllowedGroups: []string{"eng"}}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty tool")
}

func TestNewApprover_NilInnerDefaultsToAllowAll(t *testing.T) {
	// The wrapper is meant to be additive; a nil inner falls back
	// to allow-all so an operator who wires ONLY the RBAC block
	// gets the expected behaviour (RBAC filters; unlisted tools
	// pass).
	app, err := rbac.NewApprover(nil, nil)
	require.NoError(t, err)
	decision, _ := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "any"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

func TestApprove_UncoveredToolDefersToInner(t *testing.T) {
	// Tool not in rule set → inner decides. Confirms the "only
	// lock down what I explicitly named" semantics.
	inner := agent.DenyAllApprover{Reason: "inner said no"}
	app, err := rbac.NewApprover([]rbac.Rule{
		{Tool: "bash", AllowedGroups: []string{"eng"}},
	}, inner)
	require.NoError(t, err)

	decision, reason := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Equal(t, "inner said no", reason)
}

func TestApprove_MemberOfAllowedGroupDefersToInner(t *testing.T) {
	// Rule matches AND group hit → inner still decides. The
	// example inner allows, so we see the RBAC layer let the
	// request through to the inner allow-all path.
	app, err := rbac.NewApprover([]rbac.Rule{
		{Tool: "bash", AllowedGroups: []string{"eng", "sre"}},
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{
		Subject: "okta|alice",
		Groups:  []string{"eng"},
	})
	decision, _ := app.Approve(ctx, agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

func TestApprove_MemberButInnerDeniesFinalIsDeny(t *testing.T) {
	// Group hit lifts the RBAC gate but the inner (pattern /
	// prompt / etc.) still has veto. Property: RBAC is additive.
	inner := agent.DenyAllApprover{Reason: "pattern denied"}
	app, err := rbac.NewApprover([]rbac.Rule{
		{Tool: "bash", AllowedGroups: []string{"eng"}},
	}, inner)
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{
		Subject: "okta|alice",
		Groups:  []string{"eng"},
	})
	decision, reason := app.Approve(ctx, agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Equal(t, "pattern denied", reason)
}

func TestApprove_NoGroupMatchDeniesWithReason(t *testing.T) {
	app, err := rbac.NewApprover([]rbac.Rule{
		{Tool: "bash", AllowedGroups: []string{"sre"}},
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{
		Subject: "okta|alice",
		Groups:  []string{"eng"}, // "sre" required, not member
	})
	decision, reason := app.Approve(ctx, agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "okta|alice")
	assert.Contains(t, reason, "sre")
	assert.Contains(t, reason, "bash")
}

func TestApprove_AnonymousRequestDenied(t *testing.T) {
	// No identity in ctx → treated as "no groups" → any rule
	// covering the tool denies. Load-bearing fail-CLOSED
	// property: an unauthenticated caller can't slip past.
	app, err := rbac.NewApprover([]rbac.Rule{
		{Tool: "bash", AllowedGroups: []string{"eng"}},
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	decision, reason := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "anonymous")
}

func TestApprove_ToolNameCaseInsensitive(t *testing.T) {
	// Model tool names come in with various casings depending on
	// the provider. Rule match must be case-insensitive so an
	// operator writing "bash" matches "Bash" / "BASH".
	app, err := rbac.NewApprover([]rbac.Rule{
		{Tool: "Bash", AllowedGroups: []string{"eng"}},
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{
		Subject: "okta|alice", Groups: []string{"eng"},
	})
	decision, _ := app.Approve(ctx, agent.ApprovalRequest{ToolName: "BASH"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

func TestApprove_EmptyAllowedGroupsDeniesAll(t *testing.T) {
	// A rule with no allowed groups matches no one — even a
	// user with a full group list gets denied. "Explicitly
	// locked, no exceptions" semantic.
	app, err := rbac.NewApprover([]rbac.Rule{
		{Tool: "delete", AllowedGroups: nil},
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{
		Subject: "okta|admin",
		Groups:  []string{"eng", "sre", "admin"},
	})
	decision, _ := app.Approve(ctx, agent.ApprovalRequest{ToolName: "delete"})
	assert.Equal(t, agent.DecisionDeny, decision)
}

func TestApprove_ReasonIncludesInputContextNothingSensitive(t *testing.T) {
	// The deny reason should mention subject + tool + allowed
	// groups, and MUST NOT echo the tool input. Prevents a
	// pattern-based data-leak (imagine the model tried to write a
	// filename that contained secrets — an RBAC deny that echoed
	// the input would carry them into audit trails / user replies).
	app, err := rbac.NewApprover([]rbac.Rule{
		{Tool: "write", AllowedGroups: []string{"admin"}},
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{
		Subject: "okta|alice", Groups: []string{"eng"},
	})
	input := json.RawMessage(`{"path":"/tmp/SECRET_TOKEN_FOO=xyz","content":"hunter2"}`)
	_, reason := app.Approve(ctx, agent.ApprovalRequest{ToolName: "write", Input: input})
	assert.NotContains(t, reason, "SECRET_TOKEN_FOO")
	assert.NotContains(t, reason, "hunter2")
}

func TestContextRoundtrip(t *testing.T) {
	// The sso ctx helpers are tested in sso's own package; a
	// smoke here confirms the RBAC layer sees what SSO puts in.
	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "s"})
	got, ok := sso.IdentityFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "s", got.Subject)
}
