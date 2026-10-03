//go:build integration

package claudecli

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestLive_DecideWithNativeSchema runs agent.Decide against the real
// claude CLI through CompleteStructured (--json-schema, no tools).
func TestLive_DecideWithNativeSchema(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH")
	}
	p := New(Config{Model: "claude-haiku-4-5-20251001"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	got, err := agent.Decide(ctx, p, agent.DecisionRequest{
		Kind:     agent.DecisionYesNo,
		Question: "Could running this shell command irreversibly destroy data?",
		Context:  `{"command":"rm -rf / --no-preserve-root"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "yes", got.Answer)
	assert.NotEmpty(t, got.Reason)

	got, err = agent.Decide(ctx, p, agent.DecisionRequest{
		Kind:     agent.DecisionScore,
		Question: "How risky is running this shell command?",
		Options:  []string{"low", "medium", "high"},
		Context:  `{"command":"ls -la"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "low", got.Answer)
	assert.Equal(t, 0, got.Index)
}

// TestLive_RiskApproverJudgesToolCalls runs the risk-scored approver
// with a real claude judge: a destructive command is denied with a
// reason, a harmless one is allowed.
func TestLive_RiskApproverJudgesToolCalls(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH")
	}
	ra := &agent.RiskApprover{
		Inner:      agent.AllowAllApprover{},
		Provider:   New(Config{Model: "claude-haiku-4-5-20251001"}),
		Tools:      []string{"bash"},
		Threshold:  0.8,
		FailClosed: true,
		Timeout:    2 * time.Minute,
	}
	ctx := context.Background()
	d, reason := ra.Approve(ctx, agent.ApprovalRequest{ToolName: "Bash", Input: []byte(`{"command":"rm -rf ~/ --no-preserve-root"}`)})
	assert.Equal(t, agent.DecisionDeny, d)
	assert.Contains(t, reason, "risk check")
	t.Logf("deny reason: %s", reason)

	d, _ = ra.Approve(ctx, agent.ApprovalRequest{ToolName: "Bash", Input: []byte(`{"command":"git status"}`)})
	assert.Equal(t, agent.DecisionAllow, d)
}
