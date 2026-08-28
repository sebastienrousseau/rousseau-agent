package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAllowAllApprover(t *testing.T) {
	dec, reason := AllowAllApprover{}.Approve(context.Background(), ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, DecisionAllow, dec)
	assert.Empty(t, reason)
}

func TestDenyAllApprover_DefaultReason(t *testing.T) {
	dec, reason := DenyAllApprover{}.Approve(context.Background(), ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, DecisionDeny, dec)
	assert.Equal(t, "denied by policy", reason)
}

func TestDenyAllApprover_CustomReason(t *testing.T) {
	dec, reason := DenyAllApprover{Reason: "no shell"}.Approve(context.Background(), ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, DecisionDeny, dec)
	assert.Equal(t, "no shell", reason)
}

func TestApproverFunc_Adapts(t *testing.T) {
	var fn ApproverFunc = func(_ context.Context, req ApprovalRequest) (Decision, string) {
		if req.ToolName == "bash" {
			return DecisionDeny, "blocked"
		}
		return DecisionAllow, ""
	}
	dec, _ := fn.Approve(context.Background(), ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, DecisionDeny, dec)
	dec, _ = fn.Approve(context.Background(), ApprovalRequest{ToolName: "read"})
	assert.Equal(t, DecisionAllow, dec)
}

func TestPatternApprover_DenyWinsOverAllow(t *testing.T) {
	p := &PatternApprover{
		Allow:   []PatternRule{{ToolName: "bash", Match: `.*`}},
		Deny:    []PatternRule{{ToolName: "bash", Match: `rm -rf`}},
		Default: DecisionDeny,
	}
	dec, reason := p.Approve(context.Background(), ApprovalRequest{
		ToolName: "bash",
		Input:    json.RawMessage(`{"command": "rm -rf /"}`),
	})
	assert.Equal(t, DecisionDeny, dec)
	assert.NotEmpty(t, reason)

	dec, _ = p.Approve(context.Background(), ApprovalRequest{
		ToolName: "bash",
		Input:    json.RawMessage(`{"command": "ls"}`),
	})
	assert.Equal(t, DecisionAllow, dec)
}

func TestPatternApprover_DefaultDeny(t *testing.T) {
	p := &PatternApprover{Default: DecisionDeny}
	dec, _ := p.Approve(context.Background(), ApprovalRequest{ToolName: "read"})
	assert.Equal(t, DecisionDeny, dec)
}

func TestPatternApprover_DefaultAllow(t *testing.T) {
	p := &PatternApprover{Default: DecisionAllow}
	dec, _ := p.Approve(context.Background(), ApprovalRequest{ToolName: "read"})
	assert.Equal(t, DecisionAllow, dec)
}

func TestPatternApprover_ToolNameOnlyMatch(t *testing.T) {
	p := &PatternApprover{
		Allow:   []PatternRule{{ToolName: "read"}},
		Default: DecisionDeny,
	}
	dec, _ := p.Approve(context.Background(), ApprovalRequest{ToolName: "read", Input: json.RawMessage(`{}`)})
	assert.Equal(t, DecisionAllow, dec)
	dec, _ = p.Approve(context.Background(), ApprovalRequest{ToolName: "bash", Input: json.RawMessage(`{}`)})
	assert.Equal(t, DecisionDeny, dec)
}

func TestPatternApprover_BadRegex(t *testing.T) {
	p := &PatternApprover{
		Allow: []PatternRule{{ToolName: "bash", Match: `(`}},
	}
	dec, reason := p.Approve(context.Background(), ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, DecisionDeny, dec)
	assert.Contains(t, reason, "compile")
}

func TestPatternApprover_EmptyToolNameMatchesAll(t *testing.T) {
	p := &PatternApprover{
		Deny:    []PatternRule{{Match: `secret`}},
		Default: DecisionAllow,
	}
	dec, _ := p.Approve(context.Background(), ApprovalRequest{
		ToolName: "read", Input: json.RawMessage(`{"path":"/tmp/secret"}`),
	})
	assert.Equal(t, DecisionDeny, dec)
}
