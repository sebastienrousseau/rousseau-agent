package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// riskQuestion is what RiskApprover asks about each scored tool call.
const riskQuestion = "Could executing this tool call cause irreversible damage, destroy or " +
	"overwrite data beyond what a routine coding task needs, expose secrets or credentials, " +
	"send data to an external party, or affect systems other than the user's own workspace?"

// RiskApprover adds a model-scored check on top of a deterministic
// approver chain. For the listed tools, a call the inner chain allows
// is judged by Decide (yes/no + confidence); a "yes" at or above
// Threshold is denied with the model's reason. It never overrides an
// inner deny and never calls the model when the inner chain denied.
//
// The verdict is advisory evidence from a model, so the gate only
// subtracts: it can deny, never allow what the chain refused. A judge
// failure (timeout, provider error) denies when FailClosed is set.
type RiskApprover struct {
	Inner    Approver
	Provider Provider
	// Tools are the tool names to score (case-insensitive). Empty
	// scores every tool.
	Tools []string
	// Threshold is the minimum confidence of a "risky" verdict that
	// denies the call, in (0, 1].
	Threshold float64
	// FailClosed denies the call when the judge cannot answer.
	FailClosed bool
	// Timeout bounds one judgement.
	Timeout time.Duration
}

// Approve satisfies Approver.
func (r *RiskApprover) Approve(ctx context.Context, req ApprovalRequest) (Decision, string) {
	if d, reason := r.Inner.Approve(ctx, req); d == DecisionDeny {
		return d, reason
	}
	if !r.scores(req.ToolName) {
		return DecisionAllow, ""
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	verdict, err := Decide(ctx, r.Provider, DecisionRequest{
		Kind:     DecisionYesNo,
		Question: riskQuestion,
		Context:  "tool: " + req.ToolName + "\ninput: " + string(req.Input),
	})
	if err != nil {
		if r.FailClosed {
			return DecisionDeny, "risk check failed, so the call is blocked: " + err.Error()
		}
		return DecisionAllow, ""
	}
	if verdict.Answer == "yes" && verdict.Confidence >= r.Threshold {
		return DecisionDeny, fmt.Sprintf("blocked by risk check (confidence %.2f): %s", verdict.Confidence, verdict.Reason)
	}
	return DecisionAllow, ""
}

func (r *RiskApprover) scores(tool string) bool {
	if len(r.Tools) == 0 {
		return true
	}
	for _, t := range r.Tools {
		if strings.EqualFold(strings.TrimSpace(t), tool) {
			return true
		}
	}
	return false
}
