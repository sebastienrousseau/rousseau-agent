// Package rbac implements the group-based access-control approver
// wrapper — first slice of ROADMAP §2.9 (governance-advanced).
// Gated at daemon-assembly time on [license.FeatureGovernanceAdvanced];
// this package itself is licence-agnostic.
//
// # Model
//
// Every rule maps a tool name to a set of allowed groups. When the
// approver is asked about a tool covered by a rule:
//
//   - If the request carries an SSO identity whose Groups slice
//     contains ANY of the rule's allowed groups → defer to the
//     wrapped inner approver (pattern rules / TUI prompt / etc.)
//   - Otherwise → DENY with a "governance: not in group ..." reason.
//
// Rules for tools NOT explicitly listed are pass-through — the
// wrapped approver still runs. This matches operator intuition:
// "only lock down what I explicitly named."
//
// # Fail-CLOSED discipline
//
// Anonymous requests (no SSO identity in ctx) are treated as
// "no groups". Any rule covering a tool the anonymous caller wants
// will deny — matches the enterprise expectation that a governance
// gate hard-fails without an authenticated identity.
//
// # Why wrap (not replace)
//
// The existing PatternApprover + TUI approver are the OSS default
// and cover a large fraction of production installs. Wrapping keeps
// them working; RBAC is an additive filter that lands ONLY when
// the licence unlocks it.
package rbac

import (
	"context"
	"fmt"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// Rule maps one tool name to the list of groups permitted to
// invoke it. Empty AllowedGroups means the rule matches no one —
// use it as an "explicitly locked, no exceptions" marker.
type Rule struct {
	// Tool is the model-facing tool name (e.g. "bash", "write").
	// Case-insensitive match against [agent.ApprovalRequest.ToolName].
	Tool string
	// AllowedGroups is the whitelist of SSO groups (from
	// [sso.Identity.Groups]) permitted to invoke the tool. Any
	// membership match grants; no match denies.
	AllowedGroups []string
}

// Approver is the RBAC wrapper. Constructed via [NewApprover].
// Safe for concurrent use — the rule set is frozen at
// construction.
type Approver struct {
	rules map[string]map[string]struct{} // tool (lower) → group set
	inner agent.Approver
}

// NewApprover wraps inner with a group-membership check on the
// listed rules. A nil inner uses [agent.AllowAllApprover] — the
// RBAC layer is meant to be additive, not the sole gate. Callers
// that want deny-by-default without an inner should compose
// [agent.DenyAllApprover] explicitly.
//
// Returns an error if any rule names an empty tool.
func NewApprover(rules []Rule, inner agent.Approver) (*Approver, error) {
	if inner == nil {
		inner = agent.AllowAllApprover{}
	}
	byTool := make(map[string]map[string]struct{}, len(rules))
	for _, r := range rules {
		if strings.TrimSpace(r.Tool) == "" {
			return nil, fmt.Errorf("rbac: rule with empty tool name")
		}
		set := make(map[string]struct{}, len(r.AllowedGroups))
		for _, g := range r.AllowedGroups {
			set[strings.TrimSpace(g)] = struct{}{}
		}
		byTool[strings.ToLower(strings.TrimSpace(r.Tool))] = set
	}
	return &Approver{rules: byTool, inner: inner}, nil
}

// Approve satisfies [agent.Approver].
func (a *Approver) Approve(ctx context.Context, req agent.ApprovalRequest) (agent.Decision, string) {
	rule, covered := a.rules[strings.ToLower(req.ToolName)]
	if !covered {
		// Tool isn't listed — defer to the inner approver.
		return a.inner.Approve(ctx, req)
	}
	id, _ := sso.IdentityFromContext(ctx)
	for _, g := range id.Groups {
		if _, ok := rule[strings.TrimSpace(g)]; ok {
			// Group hit — allowed to attempt; inner still decides
			// whether the specific input is OK (bash pattern rules,
			// TUI prompt, etc.).
			return a.inner.Approve(ctx, req)
		}
	}
	who := id.Subject
	if who == "" {
		who = "anonymous"
	}
	allowed := make([]string, 0, len(rule))
	for g := range rule {
		allowed = append(allowed, g)
	}
	return agent.DecisionDeny, fmt.Sprintf(
		"governance: %s lacks required group for tool %q (needs one of: %s)",
		who, req.ToolName, strings.Join(allowed, ", "),
	)
}

// Compile-time interface satisfaction.
var _ agent.Approver = (*Approver)(nil)
