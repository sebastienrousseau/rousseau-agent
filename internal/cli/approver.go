package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// buildApprover translates the ApproverConfig into an agent.Approver.
// Returns AllowAllApprover when unset — matches the current default
// behaviour, so pre-approver configs keep running.
func buildApprover(cfg config.ApproverConfig) (agent.Approver, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch mode {
	case "", "allow_all", "allow":
		return agent.AllowAllApprover{}, nil
	case "deny_all", "deny":
		return agent.DenyAllApprover{Reason: cfg.Reason}, nil
	case "pattern":
		var def agent.Decision
		switch strings.ToLower(cfg.Default) {
		case "allow":
			def = agent.DecisionAllow
		case "", "deny":
			def = agent.DecisionDeny
		default:
			return nil, fmt.Errorf("approver: unknown default %q", cfg.Default)
		}
		return &agent.PatternApprover{
			Allow:      toRules(cfg.Allow),
			Deny:       toRules(cfg.Deny),
			DenyReason: cfg.Reason,
			Default:    def,
		}, nil
	default:
		return nil, fmt.Errorf("approver: unknown mode %q (want allow_all / deny_all / pattern)", cfg.Mode)
	}
}

func toRules(in []config.PatternEntry) []agent.PatternRule {
	out := make([]agent.PatternRule, 0, len(in))
	for _, e := range in {
		out = append(out, agent.PatternRule{ToolName: e.Tool, Match: e.Match})
	}
	return out
}

// chainApprovers runs first, and only consults second when first
// returns DecisionAllow. This lets the CLI wrap the config-driven
// PatternApprover (or AllowAll / DenyAll) around the interactive
// TUI approver so:
//
//   - a Deny from first (e.g. blanket-deny `bash`) short-circuits
//     the interactive prompt entirely — the user never sees a
//     question they can only answer one way.
//   - an Allow from first still prompts the user (interactive
//     approver runs second) so the operator retains veto over
//     specific inputs even for tools that policy pre-approves in
//     principle. To auto-approve without prompting, users answer
//     [a] on the first interactive prompt for that tool.
//
// When first is AllowAll (the config default), the behaviour reduces
// to "always prompt via second" — matching the CLI's stated intent
// that the interactive user is the authority in a TUI session.
func chainApprovers(first, second agent.Approver) agent.Approver {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	if _, ok := first.(agent.AllowAllApprover); ok {
		// Fast path: no policy → just the interactive approver.
		return second
	}
	return agent.ApproverFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.Decision, string) {
		if d, reason := first.Approve(ctx, req); d == agent.DecisionDeny {
			return d, reason
		}
		return second.Approve(ctx, req)
	})
}
