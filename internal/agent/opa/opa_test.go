package opa_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/opa"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// -- construction --

func TestNewApprover_MissingPolicyRejected(t *testing.T) {
	_, err := opa.NewApprover(context.Background(), opa.Config{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Policy is required")
}

func TestNewApprover_BadPolicyRejected(t *testing.T) {
	// Malformed Rego → compile error at construction so the
	// daemon can WARN + fall back to inner rather than
	// discover the failure per-tool-call.
	_, err := opa.NewApprover(context.Background(), opa.Config{
		Policy: "this is not valid rego {{{",
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rego")
}

func TestNewApprover_NilInnerDefaultsToAllowAll(t *testing.T) {
	// Same pattern as rbac.NewApprover — a nil inner falls
	// back to allow-all so an operator wiring ONLY the OPA
	// block sees "OPA allows → tool runs".
	app, err := opa.NewApprover(context.Background(), opa.Config{
		Policy: `package rousseau.authz
import rego.v1
default allow := true
default reason := ""`,
	}, nil)
	require.NoError(t, err)
	decision, _ := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "any"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

// -- policy behaviour --

func TestApprove_AllowTrueDefersToInner(t *testing.T) {
	// Policy allows; inner denies; final = deny. Proves the
	// wrapper is truly additive — OPA is one gate among many.
	app, err := opa.NewApprover(context.Background(), opa.Config{
		Policy: `package rousseau.authz
import rego.v1
default allow := true
default reason := ""`,
	}, agent.DenyAllApprover{Reason: "inner denied"})
	require.NoError(t, err)

	decision, reason := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Equal(t, "inner denied", reason)
}

func TestApprove_DenyReturnsGovernancePrefixedReason(t *testing.T) {
	// Denial reason must include the "governance:" prefix so
	// SIEM filters can slice audit records by policy layer
	// (matches rbac's convention).
	app, err := opa.NewApprover(context.Background(), opa.Config{
		Policy: `package rousseau.authz
import rego.v1
default allow := false
default reason := "policy says no"`,
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	decision, reason := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Equal(t, "governance: policy says no", reason)
}

func TestApprove_ToolNameInPolicy(t *testing.T) {
	// Load-bearing property: the policy can gate on the tool
	// name. Blocks bash but allows read.
	app, err := opa.NewApprover(context.Background(), opa.Config{
		Policy: `package rousseau.authz
import rego.v1
default allow := false
default reason := "tool blocked"

allow if { input.tool == "read" }`,
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	readDecision, _ := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	bashDecision, _ := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionAllow, readDecision)
	assert.Equal(t, agent.DecisionDeny, bashDecision)
}

func TestApprove_SSOGroupsInPolicy(t *testing.T) {
	// Load-bearing property: the policy can gate on the
	// caller's SSO groups. Confirms sso.IdentityFromContext
	// data reaches Rego.
	app, err := opa.NewApprover(context.Background(), opa.Config{
		Policy: `package rousseau.authz
import rego.v1
default allow := false
default reason := "not in required group"

allow if { "sre" in input.groups }`,
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	// Non-member.
	ctxOther := sso.WithIdentity(context.Background(), sso.Identity{
		Subject: "u1", Groups: []string{"eng"},
	})
	dOther, _ := app.Approve(ctxOther, agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, dOther)

	// Member.
	ctxSRE := sso.WithIdentity(context.Background(), sso.Identity{
		Subject: "u2", Groups: []string{"sre", "eng"},
	})
	dSRE, _ := app.Approve(ctxSRE, agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionAllow, dSRE)
}

func TestApprove_ToolInputParsedAsJSON(t *testing.T) {
	// Property: the model's raw JSON input is parsed so
	// policies can descend into it: `input.input.cmd == "..."`
	app, err := opa.NewApprover(context.Background(), opa.Config{
		Policy: `package rousseau.authz
import rego.v1
default allow := true
default reason := ""

allow = false if { input.input.cmd == "rm -rf /" }
reason := "no rm -rf /" if { input.input.cmd == "rm -rf /" }`,
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	req := agent.ApprovalRequest{
		ToolName: "bash",
		Input:    json.RawMessage(`{"cmd":"rm -rf /"}`),
	}
	decision, reason := app.Approve(context.Background(), req)
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "no rm -rf /")

	// Different cmd → allowed.
	req.Input = json.RawMessage(`{"cmd":"ls -la"}`)
	dAllow, _ := app.Approve(context.Background(), req)
	assert.Equal(t, agent.DecisionAllow, dAllow)
}

func TestApprove_MalformedJSONInputStillReachesPolicy(t *testing.T) {
	// Property: bad-JSON tool input doesn't crash the
	// approver. The policy sees the raw string; a policy that
	// wants "input.input" to be an object will deny (which is
	// the correct disposition for garbage input).
	app, err := opa.NewApprover(context.Background(), opa.Config{
		Policy: `package rousseau.authz
import rego.v1
default allow := true
default reason := ""`,
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	req := agent.ApprovalRequest{
		ToolName: "bash",
		Input:    json.RawMessage(`not-json`),
	}
	require.NotPanics(t, func() {
		decision, _ := app.Approve(context.Background(), req)
		assert.Equal(t, agent.DecisionAllow, decision)
	})
}

func TestApprove_AnonymousActorHandled(t *testing.T) {
	// Property: no SSO identity in ctx → policy sees actor=""
	// and groups=[]. A policy that requires membership denies
	// (correct fail-CLOSED disposition for anonymous callers).
	app, err := opa.NewApprover(context.Background(), opa.Config{
		Policy: `package rousseau.authz
import rego.v1
default allow := false
default reason := "identity required"

allow if { input.actor != "" }`,
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	decision, reason := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "identity required")
}

// -- fail-closed on runtime errors --

func TestApprove_CtxCancelDenies(t *testing.T) {
	// A cancelled ctx must NOT slip through as allow. Rego
	// eval respects ctx; the cancellation surfaces as an eval
	// error → DENY.
	app, err := opa.NewApprover(context.Background(), opa.Config{
		Policy: `package rousseau.authz
import rego.v1
default allow := true
default reason := ""`,
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	decision, reason := app.Approve(ctx, agent.ApprovalRequest{ToolName: "read"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "governance:")
}

func TestApprove_ReasonNeverEchoesToolInput(t *testing.T) {
	// Regression guard against a data leak through the deny
	// reason. Even if the policy references input.input.path,
	// the reply string must not carry secrets from the input
	// unless the policy author writes them there explicitly.
	// (Same discipline as rbac's TestApprove_ReasonIncludesInputContextNothingSensitive.)
	app, err := opa.NewApprover(context.Background(), opa.Config{
		Policy: `package rousseau.authz
import rego.v1
default allow := false
default reason := "you cannot write to that path"`,
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	req := agent.ApprovalRequest{
		ToolName: "write",
		Input:    json.RawMessage(`{"path":"/tmp/SECRET_TOKEN=xyz","content":"hunter2"}`),
	}
	_, reason := app.Approve(context.Background(), req)
	assert.NotContains(t, reason, "SECRET_TOKEN")
	assert.NotContains(t, reason, "hunter2")
}

func TestApprove_PolicyResultNotObjectDenies(t *testing.T) {
	// Defensive: a policy that returns a non-object value at
	// the query root (operator mistake) MUST deny, not allow.
	app, err := opa.NewApprover(context.Background(), opa.Config{
		Query: "data.rousseau.authz.allow", // returns bool directly, not the recommended object shape
		Policy: `package rousseau.authz
import rego.v1
default allow := true`,
	}, agent.AllowAllApprover{})
	require.NoError(t, err)

	decision, reason := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "not an object")
}
