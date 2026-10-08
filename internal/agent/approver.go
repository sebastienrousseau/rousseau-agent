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
// regular expression over the input. Empty ToolName matches every
// tool. An allow rule compares the tool name exactly (case-sensitive),
// the same way the tool registry resolves it; a deny rule compares it
// case-insensitively, the fail-safe direction.
//
// With Field empty, Match is an unanchored regular expression over the
// canonical input JSON (see [CanonicalInput]); empty Match matches every
// input. With Field set, Match is anchored to the whole value
// (^(?:Match)$) and tested against that top-level string field of the
// decoded input; a missing or non-string field never matches. The
// field name is compared case-insensitively, as encoding/json does when
// a tool decodes its input into a struct.
type PatternRule struct {
	ToolName string
	Match    string
	Field    string
}

// PatternApprover applies allow / deny rules against a request. Deny
// rules take precedence over allow rules — the safer disposition wins.
// If no rule matches, PatternApprover defers to Default. Input that is
// not valid JSON or repeats a key is denied outright.
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
	// foldTool compares the tool name case-insensitively. Set for deny
	// rules only: matching more tools is the safe direction for a deny,
	// and it keeps a "bash" deny covering the claude CLI's "Bash" on the
	// external-tool bridge. Allow rules compare exactly, so an allow for
	// mcp:x:run never grants a different tool mcp:x:RUN.
	foldTool bool
	field    string
	match    *regexp.Regexp
}

func compileRules(rules []PatternRule, foldTool bool) ([]compiledRule, error) {
	out := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		expr := r.Match
		if r.Field != "" {
			expr = `^(?:` + r.Match + `)$`
		}
		var re *regexp.Regexp
		if expr != "" {
			var err error
			if re, err = regexp.Compile(expr); err != nil {
				return nil, err
			}
		}
		out = append(out, compiledRule{toolName: r.ToolName, foldTool: foldTool, field: r.Field, match: re})
	}
	return out, nil
}

// patternInput is one request's input in the two forms rules read:
// the canonical bytes and, decoded on first use, the top-level object.
type patternInput struct {
	canonical json.RawMessage
	decoded   bool
	object    map[string]any
}

// field returns the top-level string field whose name equals name
// under case folding. CanonicalInput already rejected inputs where two
// keys fold together, so at most one key can match.
func (in *patternInput) field(name string) (string, bool) {
	if !in.decoded {
		in.decoded = true
		_ = json.Unmarshal(in.canonical, &in.object) //nolint:errcheck // a non-object input leaves object nil: no field matches
	}
	for k, v := range in.object {
		if strings.EqualFold(k, name) {
			s, ok := v.(string)
			return s, ok
		}
	}
	return "", false
}

func (r compiledRule) toolMatches(toolName string) bool {
	switch {
	case r.toolName == "":
		return true
	case r.foldTool:
		return strings.EqualFold(r.toolName, toolName)
	default:
		return r.toolName == toolName
	}
}

func (r compiledRule) matches(toolName string, in *patternInput) bool {
	if !r.toolMatches(toolName) {
		return false
	}
	if r.field != "" {
		v, ok := in.field(r.field)
		return ok && r.match.MatchString(v)
	}
	if r.match == nil {
		return true
	}
	return r.match.Match(in.canonical)
}

// Approve satisfies Approver.
func (p *PatternApprover) Approve(_ context.Context, req ApprovalRequest) (Decision, string) {
	p.once.Do(func() {
		if p.compiled.allow, p.compileErr = compileRules(p.Allow, false); p.compileErr != nil {
			return
		}
		p.compiled.deny, p.compileErr = compileRules(p.Deny, true)
	})
	if p.compileErr != nil {
		return DecisionDeny, "approver: pattern compile: " + p.compileErr.Error()
	}
	canonical, err := CanonicalInput(req.Input)
	if err != nil {
		return DecisionDeny, ErrNonCanonicalInput.Error()
	}
	in := &patternInput{canonical: canonical}
	// Deny wins over allow — safer disposition.
	if anyRuleMatches(p.compiled.deny, req.ToolName, in) {
		return DecisionDeny, p.denyReason()
	}
	if anyRuleMatches(p.compiled.allow, req.ToolName, in) || p.Default == DecisionAllow {
		return DecisionAllow, ""
	}
	return DecisionDeny, p.denyReason()
}

func anyRuleMatches(rules []compiledRule, toolName string, in *patternInput) bool {
	for _, rule := range rules {
		if rule.matches(toolName, in) {
			return true
		}
	}
	return false
}

func (p *PatternApprover) denyReason() string {
	if p.DenyReason == "" {
		return "denied by pattern policy"
	}
	return p.DenyReason
}
