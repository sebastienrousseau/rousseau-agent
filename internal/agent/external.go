package agent

import (
	"context"
	"log/slog"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
)

// DecideExternal applies this agent's tool policy to a tool call that a
// provider subprocess executes itself (the claude CLI runs Bash, Edit,
// Write and MCP tools in its own loop). It runs the same Approver chain
// and PreToolUse hooks as a native call and records the same audit
// events, tagged executor=external, so governance and the audit trail
// cover the default claudecli path too. The caller blocks the call on
// DecisionDeny and relays reason to the model.
func (a *Agent) DecideExternal(ctx context.Context, req ApprovalRequest) (Decision, string) {
	detail := func(extra map[string]any) map[string]any {
		d := map[string]any{"executor": "external"}
		for k, v := range extra {
			d[k] = v
		}
		return d
	}
	deny := func(label, result, reason string) (Decision, string) {
		observability.ToolCalls.WithLabelValues(req.ToolName, label).Inc()
		a.logger.Warn("tool.denied", slog.String("name", req.ToolName),
			slog.String("reason", reason), slog.String("executor", "external"))
		a.emitToolAudit(ctx, "deny", req.ToolName, result, req.SessionID,
			detail(map[string]any{"reason": reason}))
		return DecisionDeny, reason
	}

	if decision, reason := a.opts.Approver.Approve(ctx, req); decision == DecisionDeny {
		if reason == "" {
			reason = "denied by policy"
		}
		return deny("deny", "denied", reason)
	}
	if a.opts.Hooks != nil {
		payload, err := hooks.MarshalPreToolUse(req.SessionID, req.ToolName, req.Input)
		if err == nil {
			verdict, herr := a.opts.Hooks.Run(ctx, hooks.EventPreToolUse, payload)
			if herr != nil {
				a.logger.Warn("hook.run_failed", slog.String("event", string(hooks.EventPreToolUse)), slog.String("err", herr.Error()))
			}
			switch verdict.Decision {
			case hooks.DecisionDeny:
				reason := verdict.Reason
				if reason == "" {
					reason = "denied by hook"
				}
				return deny("hook_deny", "hook_denied", reason)
			case hooks.DecisionModify:
				// The subprocess owns the input and cannot take a
				// rewrite; a hook that wanted one did not accept the
				// original.
				return deny("hook_deny", "hook_denied",
					"a PreToolUse hook asked to modify this input, which cannot be applied to externally executed tools")
			}
		}
	}
	observability.ToolCalls.WithLabelValues(req.ToolName, "external_allow").Inc()
	a.emitToolAudit(ctx, "allow", req.ToolName, "allowed", req.SessionID, detail(nil))
	return DecisionAllow, ""
}
