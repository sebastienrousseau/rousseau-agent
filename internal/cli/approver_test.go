package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestBuildApprover_EmptyDefaultsAllowAll(t *testing.T) {
	got, err := buildApprover(config.ApproverConfig{})
	require.NoError(t, err)
	_, ok := got.(agent.AllowAllApprover)
	assert.True(t, ok)
}

func TestBuildApprover_AllowAllExplicit(t *testing.T) {
	got, err := buildApprover(config.ApproverConfig{Mode: "allow_all"})
	require.NoError(t, err)
	dec, _ := got.Approve(context.Background(), agent.ApprovalRequest{})
	assert.Equal(t, agent.DecisionAllow, dec)
}

func TestBuildApprover_DenyAll(t *testing.T) {
	got, err := buildApprover(config.ApproverConfig{Mode: "deny_all", Reason: "nope"})
	require.NoError(t, err)
	dec, reason := got.Approve(context.Background(), agent.ApprovalRequest{})
	assert.Equal(t, agent.DecisionDeny, dec)
	assert.Equal(t, "nope", reason)
}

func TestBuildApprover_PatternWithAllow(t *testing.T) {
	got, err := buildApprover(config.ApproverConfig{
		Mode:    "pattern",
		Default: "deny",
		Allow:   []config.PatternEntry{{Tool: "read"}},
	})
	require.NoError(t, err)
	dec, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read", Input: json.RawMessage(`{}`)})
	assert.Equal(t, agent.DecisionAllow, dec)
	dec, _ = got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash", Input: json.RawMessage(`{}`)})
	assert.Equal(t, agent.DecisionDeny, dec)
}

func TestBuildApprover_PatternDefaultAllow(t *testing.T) {
	got, err := buildApprover(config.ApproverConfig{Mode: "pattern", Default: "allow"})
	require.NoError(t, err)
	dec, _ := got.Approve(context.Background(), agent.ApprovalRequest{ToolName: "anything"})
	assert.Equal(t, agent.DecisionAllow, dec)
}

func TestBuildApprover_UnknownDefault(t *testing.T) {
	_, err := buildApprover(config.ApproverConfig{Mode: "pattern", Default: "yolo"})
	assert.Error(t, err)
}

func TestBuildApprover_UnknownMode(t *testing.T) {
	_, err := buildApprover(config.ApproverConfig{Mode: "interactive"})
	assert.Error(t, err)
}

func TestToRules_Conversion(t *testing.T) {
	got := toRules([]config.PatternEntry{
		{Tool: "bash", Match: "rm -rf"},
		{Tool: "read", Match: ""},
	})
	require.Len(t, got, 2)
	assert.Equal(t, "bash", got[0].ToolName)
	assert.Equal(t, "rm -rf", got[0].Match)
}

// stubApprover is a canned-response agent.Approver used to prove the
// short-circuit and fall-through paths in chainApprovers.
type stubApprover struct {
	decision agent.Decision
	reason   string
	called   int
}

func (s *stubApprover) Approve(_ context.Context, _ agent.ApprovalRequest) (agent.Decision, string) {
	s.called++
	return s.decision, s.reason
}

func TestChainApprovers_FirstDenyShortCircuits(t *testing.T) {
	// The interactive approver must NOT be prompted when the
	// config-driven policy already denies — the user would face a
	// question they can only answer one way.
	first := &stubApprover{decision: agent.DecisionDeny, reason: "policy: bash blocked"}
	second := &stubApprover{decision: agent.DecisionAllow}
	chained := chainApprovers(first, second)

	d, reason := chained.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, d)
	assert.Equal(t, "policy: bash blocked", reason)
	assert.Equal(t, 1, first.called)
	assert.Equal(t, 0, second.called, "interactive approver must not fire when first denies")
}

func TestChainApprovers_FirstAllowFallsThrough(t *testing.T) {
	// A policy allow does NOT auto-approve — the interactive
	// approver still runs so the operator retains veto over
	// specific inputs.
	first := &stubApprover{decision: agent.DecisionAllow}
	second := &stubApprover{decision: agent.DecisionDeny, reason: "user rejected"}
	chained := chainApprovers(first, second)

	d, reason := chained.Approve(context.Background(), agent.ApprovalRequest{ToolName: "grep"})
	assert.Equal(t, agent.DecisionDeny, d)
	assert.Equal(t, "user rejected", reason)
	assert.Equal(t, 1, first.called)
	assert.Equal(t, 1, second.called)
}

func TestChainApprovers_AllowAllIsFastPathToSecond(t *testing.T) {
	// AllowAllApprover is the config default. In that common case
	// chainApprovers must return the second approver directly —
	// avoiding an unnecessary function-value indirection AND
	// avoiding a needless call.Approve() on every prompt.
	second := &stubApprover{decision: agent.DecisionAllow}
	got := chainApprovers(agent.AllowAllApprover{}, second)
	assert.Same(t, agent.Approver(second), got, "AllowAll → chain reduces to just second")
}

func TestChainApprovers_NilFallbacks(t *testing.T) {
	// Defensive: nil either arg returns the other. Neither prod path
	// hits this — but locking the shape keeps a future refactor from
	// silently swallowing an approver.
	second := &stubApprover{}
	assert.Equal(t, agent.Approver(second), chainApprovers(nil, second))
	first := &stubApprover{}
	assert.Equal(t, agent.Approver(first), chainApprovers(first, nil))
}
