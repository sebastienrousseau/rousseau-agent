package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// TestDecideExternal_AppliesApproverAndAudits pins the governance
// bridge contract: a tool call claude runs on its own goes through the
// same Approver as a native call and leaves the same audit trail.
func TestDecideExternal_AppliesApproverAndAudits(t *testing.T) {
	audit := &captureAuditSink{}
	ag := New(&stubProvider{}, tools.NewRegistry(), silentLogger(), Options{
		Approver: &PatternApprover{
			Deny:    []PatternRule{{ToolName: "bash", Match: `rm -rf`}},
			Default: DecisionAllow,
		},
		AuditSink: audit,
	})
	ctx := context.Background()

	d, reason := ag.DecideExternal(ctx, ApprovalRequest{
		ToolName: "Bash", Input: json.RawMessage(`{"command":"rm -rf /"}`), SessionID: "s-1",
	})
	assert.Equal(t, DecisionDeny, d, "claude's Bash matches a bash rule case-insensitively")
	assert.NotEmpty(t, reason)

	d, _ = ag.DecideExternal(ctx, ApprovalRequest{
		ToolName: "Bash", Input: json.RawMessage(`{"command":"ls"}`), SessionID: "s-1",
	})
	assert.Equal(t, DecisionAllow, d)

	recs := audit.snapshot()
	require.Len(t, recs, 2)
	assert.Equal(t, "tool_call", recs[0].Category)
	assert.Equal(t, "deny", recs[0].Verb)
	assert.Equal(t, "Bash", recs[0].Object)
	assert.Equal(t, "s-1", recs[0].Detail["session_id"])
	assert.Equal(t, "external", recs[0].Detail["executor"])
	assert.Equal(t, "allow", recs[1].Verb)
}
