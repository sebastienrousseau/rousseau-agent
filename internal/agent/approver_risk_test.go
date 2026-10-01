package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type riskJudge struct {
	stubCompProvider
	answer string
	conf   float64
	err    error
	n      atomic.Int32
}

func (r *riskJudge) CompleteStructured(context.Context, Request, map[string]any) (json.RawMessage, error) {
	r.n.Add(1)
	if r.err != nil {
		return nil, r.err
	}
	b, _ := json.Marshal(map[string]any{"answer": r.answer, "confidence": r.conf, "reason": "would delete user data"}) //nolint:errcheck // test fixture
	return b, nil
}

func bashCall(cmd string) ApprovalRequest {
	return ApprovalRequest{ToolName: "Bash", Input: json.RawMessage(`{"command":"` + cmd + `"}`), SessionID: "s"}
}

// TestRiskApprover pins the risk-scored gate: it only ever adds
// denials on top of the deterministic chain, scores only the listed
// tools, denies a confident "risky" verdict with the model's reason,
// and fails closed when the judge errors (unless configured open).
func TestRiskApprover(t *testing.T) {
	ctx := context.Background()
	mk := func(j *riskJudge, inner Approver) *RiskApprover {
		return &RiskApprover{Inner: inner, Provider: j, Tools: []string{"bash"}, Threshold: 0.8, FailClosed: true, Timeout: time.Second}
	}

	j := &riskJudge{answer: "yes", conf: 0.95}
	d, reason := mk(j, AllowAllApprover{}).Approve(ctx, bashCall("rm -rf ~"))
	assert.Equal(t, DecisionDeny, d)
	assert.Contains(t, reason, "would delete user data")

	j = &riskJudge{answer: "yes", conf: 0.5}
	d, _ = mk(j, AllowAllApprover{}).Approve(ctx, bashCall("rm build/x"))
	assert.Equal(t, DecisionAllow, d, "below threshold")

	j = &riskJudge{answer: "no", conf: 0.99}
	d, _ = mk(j, AllowAllApprover{}).Approve(ctx, bashCall("ls"))
	assert.Equal(t, DecisionAllow, d)

	j = &riskJudge{answer: "no", conf: 0.99}
	d, _ = mk(j, AllowAllApprover{}).Approve(ctx, ApprovalRequest{ToolName: "Read"})
	assert.Equal(t, DecisionAllow, d)
	assert.Zero(t, j.n.Load(), "unlisted tools are not scored")

	j = &riskJudge{answer: "no", conf: 0.99}
	d, _ = mk(j, DenyAllApprover{Reason: "pattern"}).Approve(ctx, bashCall("ls"))
	assert.Equal(t, DecisionDeny, d, "never overrides a deterministic deny")
	assert.Zero(t, j.n.Load(), "no model call when the chain already denied")

	j = &riskJudge{err: errors.New("judge unavailable")}
	d, reason = mk(j, AllowAllApprover{}).Approve(ctx, bashCall("ls"))
	assert.Equal(t, DecisionDeny, d, "fails closed")
	assert.Contains(t, reason, "risk check failed")
	open := mk(j, AllowAllApprover{})
	open.FailClosed = false
	d, _ = open.Approve(ctx, bashCall("ls"))
	assert.Equal(t, DecisionAllow, d, "fail-open when configured")
}
