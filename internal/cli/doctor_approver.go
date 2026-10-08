package cli

import (
	"fmt"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// checkApproverPatterns warns once per pattern-mode allow rule that
// has no `field`. Such a rule is an unanchored regex over the whole
// input JSON, so an allow for `git status` also allows
// `git status; curl … | sh`. A `field` rule is anchored to one string
// field and has no such gap. Deny rules are not warned: matching
// anywhere in the input is the safe direction for a deny.
func checkApproverPatterns(cfg *config.Config) []diagResult {
	if !strings.EqualFold(strings.TrimSpace(cfg.Agent.Approver.Mode), "pattern") {
		return nil
	}
	var out []diagResult
	for i, rule := range cfg.Agent.Approver.Allow {
		if rule.Field != "" {
			continue
		}
		tool := rule.Tool
		if tool == "" {
			tool = "any tool"
		}
		out = append(out, diagResult{
			Name:   fmt.Sprintf("agent.approver.pattern.allow[%d]", i),
			Status: "warn",
			Detail: fmt.Sprintf("allow rule for %s (match %q) has no field: it is an unanchored match over the whole input; set field to anchor it to one value", tool, rule.Match),
		})
	}
	return out
}
