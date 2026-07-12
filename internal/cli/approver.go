package cli

import (
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
