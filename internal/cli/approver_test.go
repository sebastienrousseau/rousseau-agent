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
