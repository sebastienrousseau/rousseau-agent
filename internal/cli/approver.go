package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/approval"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/opa"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/rbac"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/license"
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

// wrapWithRBAC layers the group-based RBAC approver on top of the
// mode-selected inner approver when (a) the operator configured
// rbac.rules AND (b) the licence unlocks
// [license.FeatureGovernanceAdvanced].
//
// Configured but unenforceable (no licence, or a rule that does not
// build) fails closed on the governed tools: calls to the tools the
// rules name are denied with the cause, every other tool still goes
// to inner, and the daemon stays up. Licence without config →
// nothing to enforce, inner returned unchanged.
func wrapWithRBAC(inner agent.Approver, cfg config.RBACConfig, checker license.Checker, logger *slog.Logger) agent.Approver {
	if logger == nil {
		logger = slog.Default()
	}
	if len(cfg.Rules) == 0 {
		return inner
	}
	if checker == nil || !checker.IsEnabled(license.FeatureGovernanceAdvanced) {
		logger.Error("approver.rbac.licence_required",
			slog.Int("rule_count", len(cfg.Rules)),
			slog.String("feature", string(license.FeatureGovernanceAdvanced)),
			slog.String("effect", "governed tools are denied until the licence unlocks the rules"),
			slog.String("hint", "add ROUSSEAU_LICENSE_KEY with governance_advanced to activate; see docs/COMMERCIAL.md"),
		)
		return denyGoverned(inner, rbacTools(cfg), "RBAC rules are configured but not licensed")
	}
	rules := make([]rbac.Rule, len(cfg.Rules))
	for i, r := range cfg.Rules {
		rules[i] = rbac.Rule{Tool: r.Tool, AllowedGroups: append([]string(nil), r.AllowedGroups...)}
	}
	wrapped, err := rbac.NewApprover(rules, inner)
	if err != nil {
		logger.Error("approver.rbac.build_failed",
			slog.String("err", err.Error()),
			slog.String("effect", "governed tools are denied until the rules build"),
			slog.String("hint", "check agent.approver.rbac.rules for entries with an empty tool name"),
		)
		return denyGoverned(inner, rbacTools(cfg), "RBAC rules are configured but invalid: "+err.Error())
	}
	logger.Info("approver.rbac.active", slog.Int("rule_count", len(rules)))
	return wrapped
}

// wrapWithOPA layers the Rego-per-tool-call approver on top of
// inner when (a) the operator points at a policy file AND (b)
// the licence unlocks [license.FeatureGovernanceAdvanced].
// Composition intent: OPA wraps AFTER RBAC so a request must
// pass BOTH layers before reaching the mode-selected approver.
//
// No policy_file → inner returned as-is (no noise). A configured
// policy that cannot be enforced (unlicensed, missing, unreadable or
// uncompilable) fails closed: the policy governs every call, so every
// tool call is denied with the cause until it is fixed. The daemon
// stays up and still answers without tools.
func wrapWithOPA(ctx context.Context, inner agent.Approver, cfg config.OPAConfig, checker license.Checker, logger *slog.Logger) agent.Approver {
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(cfg.PolicyFile) == "" {
		return inner
	}
	if checker == nil || !checker.IsEnabled(license.FeatureGovernanceAdvanced) {
		logger.Error("approver.opa.licence_required",
			slog.String("policy_file", cfg.PolicyFile),
			slog.String("feature", string(license.FeatureGovernanceAdvanced)),
			slog.String("effect", "all tool calls are denied until the licence unlocks the policy"),
			slog.String("hint", "add ROUSSEAU_LICENSE_KEY with governance_advanced to activate; see docs/COMMERCIAL.md"),
		)
		return denyGoverned(inner, nil, "an OPA policy is configured but not licensed")
	}
	policy, err := os.ReadFile(cfg.PolicyFile) //nolint:gosec // path is operator-supplied config
	if err != nil {
		logger.Error("approver.opa.policy_read_failed",
			slog.String("policy_file", cfg.PolicyFile),
			slog.String("err", err.Error()),
			slog.String("effect", "all tool calls are denied until the policy loads"),
			slog.String("hint", "check the file exists and is readable by the daemon UID"),
		)
		return denyGoverned(inner, nil, "the OPA policy file cannot be read")
	}
	wrapped, err := opa.NewApprover(ctx, opa.Config{
		Policy:     string(policy),
		Query:      cfg.Query,
		ModuleName: cfg.PolicyFile,
	}, inner)
	if err != nil {
		logger.Error("approver.opa.compile_failed",
			slog.String("policy_file", cfg.PolicyFile),
			slog.String("err", err.Error()),
			slog.String("effect", "all tool calls are denied until the policy compiles"),
			slog.String("hint", "run `opa parse` on the policy to see the syntax error location"),
		)
		return denyGoverned(inner, nil, "the OPA policy does not compile")
	}
	logger.Info("approver.opa.active",
		slog.String("policy_file", cfg.PolicyFile),
		slog.Int("policy_bytes", len(policy)),
	)
	return wrapped
}

// wrapWithMultiParty layers the multi-party approver on top of
// inner when (a) the operator configured multi_party.rules AND
// (b) the licence unlocks [license.FeatureGovernanceAdvanced].
// Configured but unenforceable fails closed on the rule tools, as for
// wrapWithRBAC.
//
// The returned [*approval.PendingManager] is what the router
// needs to service /approve /deny chat commands — nil when the
// wrap didn't take effect (unconfigured / unlicensed / bad
// config). Callers thread nil-safely.
func wrapWithMultiParty(inner agent.Approver, cfg config.MultiPartyConfig, checker license.Checker, emitter approval.AuditEmitter, logger *slog.Logger) (agent.Approver, *approval.PendingManager) {
	if logger == nil {
		logger = slog.Default()
	}
	if len(cfg.Rules) == 0 {
		return inner, nil
	}
	if checker == nil || !checker.IsEnabled(license.FeatureGovernanceAdvanced) {
		logger.Error("approver.multi_party.licence_required",
			slog.Int("rule_count", len(cfg.Rules)),
			slog.String("feature", string(license.FeatureGovernanceAdvanced)),
			slog.String("effect", "governed tools are denied until the licence unlocks the rules"),
			slog.String("hint", "add ROUSSEAU_LICENSE_KEY with governance_advanced to activate; see docs/COMMERCIAL.md"),
		)
		return denyGoverned(inner, multiPartyTools(cfg), "multi-party approval is configured but not licensed"), nil
	}
	rules := make([]approval.Rule, len(cfg.Rules))
	for i, r := range cfg.Rules {
		rules[i] = approval.Rule{
			Tool:            r.Tool,
			NeededApprovals: r.NeededApprovals,
			Timeout:         r.Timeout,
		}
	}
	pending := approval.NewPendingManager(emitter)
	wrapped, err := approval.NewApprover(rules, inner, pending)
	if err != nil {
		logger.Error("approver.multi_party.build_failed",
			slog.String("err", err.Error()),
			slog.String("effect", "governed tools are denied until the rules build"),
			slog.String("hint", "check agent.approver.multi_party.rules for entries with an empty tool or NeededApprovals < 1"),
		)
		return denyGoverned(inner, multiPartyTools(cfg), "multi-party rules are configured but invalid: "+err.Error()), nil
	}
	logger.Info("approver.multi_party.active", slog.Int("rule_count", len(rules)))
	return wrapped, pending
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

// governedDenier fails closed for governance that is configured but
// cannot be enforced: calls to the governed tools (every tool when
// tools is nil) are denied with the cause; other calls go to inner.
type governedDenier struct {
	inner  agent.Approver
	tools  map[string]struct{}
	reason string
}

func denyGoverned(inner agent.Approver, tools []string, cause string) agent.Approver {
	d := &governedDenier{inner: inner, reason: "tool disabled: " + cause + "; see the daemon log"}
	if tools != nil {
		d.tools = make(map[string]struct{}, len(tools))
		for _, t := range tools {
			d.tools[strings.ToLower(strings.TrimSpace(t))] = struct{}{}
		}
	}
	return d
}

// Approve satisfies agent.Approver.
func (d *governedDenier) Approve(ctx context.Context, req agent.ApprovalRequest) (agent.Decision, string) {
	if d.tools == nil {
		return agent.DecisionDeny, d.reason
	}
	if _, governed := d.tools[strings.ToLower(req.ToolName)]; governed {
		return agent.DecisionDeny, d.reason
	}
	return d.inner.Approve(ctx, req)
}

func rbacTools(cfg config.RBACConfig) []string {
	out := make([]string, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		out = append(out, r.Tool)
	}
	return out
}

func multiPartyTools(cfg config.MultiPartyConfig) []string {
	out := make([]string, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		out = append(out, r.Tool)
	}
	return out
}
