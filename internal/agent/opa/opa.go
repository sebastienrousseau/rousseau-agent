// Package opa implements the Rego-per-tool-call approver — the
// second slice of ROADMAP §2.9 (governance-advanced). Landed
// after the group-based RBAC gate (see internal/agent/rbac) so
// operators who need expressive policy beyond "tool → allowed
// groups" have a first-class hook without ripping out the
// simpler layer.
//
// # Model
//
// The operator writes a Rego module and points the daemon at
// its file path. On every tool call, the agent's approver chain
// evaluates the Rego with an `input` document that carries the
// tool name, parsed tool input, session ID, and the identity of
// the requester (as populated by [sso.WithIdentity] on the
// inbound context).
//
// The policy MUST expose two decision points at the configured
// query root. Recommended shape (strict Rego v1):
//
//	package rousseau.authz
//	import rego.v1
//
//	default allow  := false
//	default reason := "no matching policy"
//
//	allow if input.tool == "read"
//	allow if "sre" in input.groups
//
//	reason := "sre-only tool" if not "sre" in input.groups
//
// On `allow == true`, this approver defers to its inner
// approver (pattern rules, TUI, ...) so the OSS layers still
// have veto. On `allow == false`, denial reason is drawn from
// the policy (`reason` field) and prefixed with "governance:"
// for consistent SIEM filtering with the RBAC approver.
//
// # Fail-CLOSED discipline
//
//   - Bad policy at construction (parse / compile error): the
//     factory returns an error — the daemon logs it as WARN and
//     falls back to the inner approver (matches wrapWithRBAC's
//     "broken config must not take the daemon offline"
//     discipline).
//   - Runtime evaluation error: DENY with the error in the
//     reason. Under a compromised OPA state (e.g. a data update
//     that corrupted the module), tool calls are refused, not
//     silently allowed.
//   - Ctx cancellation: DENY (Rego eval respects ctx; a
//     cancelled ctx is treated the same as a runtime error).
//
// # Why wrap (not replace)
//
// Same rationale as internal/agent/rbac: the OSS PatternApprover
// + TUI approver are the shipped defaults and cover the small-
// team install. OPA is an additive filter that lands ONLY when
// the licence unlocks [license.FeatureGovernanceAdvanced].
package opa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/open-policy-agent/opa/v1/rego"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// DefaultQuery is the Rego query the approver evaluates when
// Config.Query is empty. Matches the recommended module
// structure documented on the package.
const DefaultQuery = "data.rousseau.authz"

// Config configures an [Approver]. Zero-value Config is
// invalid — Policy is required.
type Config struct {
	// Policy is the Rego module source. The daemon typically
	// reads it from a file (agent.approver.opa.policy_file);
	// this type stays string-in so tests can inline policies
	// without touching the filesystem.
	Policy string
	// Query is the Rego query the approver evaluates. Empty
	// uses [DefaultQuery].
	Query string
	// ModuleName is the file-name hint used in Rego compile
	// errors. Purely cosmetic; defaults to "policy.rego".
	ModuleName string
}

// Approver evaluates a Rego policy for every tool call. Wraps
// an inner approver — construct via [NewApprover].
type Approver struct {
	query rego.PreparedEvalQuery
	inner agent.Approver
}

// NewApprover compiles cfg.Policy at cfg.Query and returns an
// [Approver] that gates inner. A nil inner uses
// [agent.AllowAllApprover] so the OPA layer alone can act as
// the sole gate for callers that want it.
//
// Returns an error on compile / parse failures — the daemon
// logs it as WARN and falls back to inner without wrapping.
func NewApprover(ctx context.Context, cfg Config, inner agent.Approver) (*Approver, error) {
	if cfg.Policy == "" {
		return nil, errors.New("opa: Config.Policy is required")
	}
	if inner == nil {
		inner = agent.AllowAllApprover{}
	}
	query := cfg.Query
	if query == "" {
		query = DefaultQuery
	}
	moduleName := cfg.ModuleName
	if moduleName == "" {
		moduleName = "policy.rego"
	}
	r := rego.New(
		rego.Query(query),
		rego.Module(moduleName, cfg.Policy),
	)
	prepared, err := r.PrepareForEval(ctx)
	if err != nil {
		return nil, fmt.Errorf("opa: prepare rego: %w", err)
	}
	return &Approver{query: prepared, inner: inner}, nil
}

// Approve satisfies [agent.Approver].
//
// The Rego evaluation input document:
//
//	{
//	  "tool":       "<tool_name>",
//	  "input":      <parsed json.RawMessage>,
//	  "session_id": "<uuid>",
//	  "actor":      "<sso subject or empty>",
//	  "groups":     [<sso groups>],
//	  "email":      "<sso email or empty>"
//	}
//
// Any field absent in the SSO identity is populated as its zero
// value so policies can safely reference `input.actor` without
// a defensive `has_key` check.
func (a *Approver) Approve(ctx context.Context, req agent.ApprovalRequest) (agent.Decision, string) {
	// Fail-CLOSED on a cancelled ctx. Rego's Eval checks ctx
	// but on a trivial policy the check races the fast path —
	// an explicit gate here guarantees cancelled callers see a
	// deny rather than a whatever-Rego-returned. Matches the
	// discipline every approver in the chain follows.
	if err := ctx.Err(); err != nil {
		return agent.DecisionDeny, "governance: opa eval: " + err.Error()
	}

	id, _ := sso.IdentityFromContext(ctx)

	// Best-effort parse of the tool input. If the model produced
	// invalid JSON (rare — providers wrap this), we still hand
	// something to Rego so a policy of "deny everything with bad
	// input" is expressible.
	var toolInput any
	if len(req.Input) > 0 {
		if err := json.Unmarshal(req.Input, &toolInput); err != nil {
			toolInput = string(req.Input)
		}
	}

	input := map[string]any{
		"tool":       req.ToolName,
		"input":      toolInput,
		"session_id": req.SessionID,
		"actor":      id.Subject,
		"groups":     groupsOrEmpty(id.Groups),
		"email":      id.Email,
	}

	rs, err := a.query.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return agent.DecisionDeny, "governance: opa eval: " + err.Error()
	}
	if len(rs) == 0 || len(rs[0].Expressions) == 0 {
		return agent.DecisionDeny, "governance: opa policy returned no result"
	}
	result, ok := rs[0].Expressions[0].Value.(map[string]any)
	if !ok {
		return agent.DecisionDeny, "governance: opa policy result is not an object"
	}
	if allow, _ := result["allow"].(bool); allow {
		return a.inner.Approve(ctx, req)
	}
	reason, _ := result["reason"].(string)
	if reason == "" {
		reason = "denied by policy"
	}
	return agent.DecisionDeny, "governance: " + reason
}

// groupsOrEmpty ensures `input.groups` is always a non-nil
// []string in the Rego input. A nil slice would arrive as `null`
// which makes idiomatic Rego (`"eng" in input.groups`) awkward.
func groupsOrEmpty(g []string) []string {
	if g == nil {
		return []string{}
	}
	return g
}

// Compile-time interface satisfaction.
var _ agent.Approver = (*Approver)(nil)
