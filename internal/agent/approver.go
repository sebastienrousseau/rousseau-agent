package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
)

// Decision is the outcome of consulting an Approver.
type Decision string

const (
	// DecisionAllow permits the tool call to execute.
	DecisionAllow Decision = "allow"
	// DecisionDeny blocks the tool call. The agent surfaces the reason
	// back to the model as a tool_result error so the model can adapt.
	DecisionDeny Decision = "deny"
)

// ApprovalRequest describes a pending tool call the agent is about to
// execute. Approvers inspect it and return a Decision.
type ApprovalRequest struct {
	// ToolName is the model-facing tool identifier (e.g. "bash").
	ToolName string
	// Input is the raw JSON input the model produced.
	Input json.RawMessage
	// SessionID identifies the conversation this call belongs to. Useful
	// for approvers that want to remember prior decisions.
	SessionID string
}

// Approver decides whether a pending tool call should execute. The
// method is called synchronously on the hot path — implementations
// must return promptly or honour ctx cancellation.
type Approver interface {
	// Approve is asked before each tool execution. Returning
	// DecisionDeny with a non-empty reason surfaces the reason to the
	// model as a tool error.
	Approve(ctx context.Context, req ApprovalRequest) (Decision, string)
}

// ApproverFunc adapts an ordinary function to Approver.
type ApproverFunc func(ctx context.Context, req ApprovalRequest) (Decision, string)

// Approve satisfies Approver.
func (f ApproverFunc) Approve(ctx context.Context, req ApprovalRequest) (Decision, string) {
	return f(ctx, req)
}

// AllowAllApprover permits every call. This is the baseline behaviour
// when no Approver is configured; use it explicitly to make that
// choice visible.
type AllowAllApprover struct{}

// Approve satisfies Approver.
func (AllowAllApprover) Approve(context.Context, ApprovalRequest) (Decision, string) {
	return DecisionAllow, ""
}

// DenyAllApprover blocks every call. Useful for smoke tests and for
// production configurations that whitelist by exception.
type DenyAllApprover struct {
	// Reason is surfaced back to the model on every denial. Empty
	// falls back to a generic "denied by policy" string.
	Reason string
}

// Approve satisfies Approver.
func (d DenyAllApprover) Approve(context.Context, ApprovalRequest) (Decision, string) {
	if d.Reason == "" {
		return DecisionDeny, "denied by policy"
	}
	return DecisionDeny, d.Reason
}

// PatternRule matches an incoming ApprovalRequest by tool name plus a
// regular expression over the input JSON. Empty ToolName matches every
// tool; empty Match matches every input.
type PatternRule struct {
	ToolName string
	Match    string
}

// PatternApprover applies allow / deny rules against a request. Deny
// rules take precedence over allow rules — the safer disposition wins.
// If no rule matches, PatternApprover defers to Default.
type PatternApprover struct {
	// Allow rules; a match grants DecisionAllow.
	Allow []PatternRule
	// Deny rules; a match returns DecisionDeny with DenyReason.
	Deny []PatternRule
	// DenyReason is surfaced back to the model on any denial. Empty
	// falls back to "denied by pattern policy".
	DenyReason string
	// Default is the disposition when no rule matches. Zero value
	// (empty Decision) is treated as DecisionDeny — safe-by-default.
	Default Decision

	once     sync.Once
	compiled struct {
		allow []compiledRule
		deny  []compiledRule
	}
	compileErr error
}

type compiledRule struct {
	toolName string
	match    *regexp.Regexp
}

func compileRules(rules []PatternRule) ([]compiledRule, error) {
	out := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		var re *regexp.Regexp
		if r.Match != "" {
			var err error
			re, err = regexp.Compile(r.Match)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, compiledRule{toolName: r.ToolName, match: re})
	}
	return out, nil
}

func (r compiledRule) matches(req ApprovalRequest) bool {
	if r.toolName != "" && !strings.EqualFold(r.toolName, req.ToolName) {
		return false
	}
	if r.match == nil {
		return true
	}
	return r.match.Match(req.Input)
}

// Approve satisfies Approver.
func (p *PatternApprover) Approve(_ context.Context, req ApprovalRequest) (Decision, string) {
	p.once.Do(func() {
		if p.compiled.allow, p.compileErr = compileRules(p.Allow); p.compileErr != nil {
			return
		}
		p.compiled.deny, p.compileErr = compileRules(p.Deny)
	})
	if p.compileErr != nil {
		return DecisionDeny, "approver: pattern compile: " + p.compileErr.Error()
	}
	// Deny wins over allow — safer disposition.
	for _, rule := range p.compiled.deny {
		if rule.matches(req) {
			return DecisionDeny, p.denyReason()
		}
	}
	for _, rule := range p.compiled.allow {
		if rule.matches(req) {
			return DecisionAllow, ""
		}
	}
	if p.Default == DecisionAllow {
		return DecisionAllow, ""
	}
	return DecisionDeny, p.denyReason()
}

func (p *PatternApprover) denyReason() string {
	if p.DenyReason == "" {
		return "denied by pattern policy"
	}
	return p.DenyReason
}
