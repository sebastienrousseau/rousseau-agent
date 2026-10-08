package cli

import (
	"context"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// isAllowAllMode reports whether the approver mode runs every tool
// call without a rule — the empty default included. Mirrors the
// allow_all spellings buildApprover accepts.
func isAllowAllMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "allow_all", "allow":
		return true
	}
	return false
}

// outboundUngated reports whether outbound tools run without any
// approval: allow_all mode with agent.approver.allow_outbound set.
func outboundUngated(cfg config.ApproverConfig) bool {
	return isAllowAllMode(cfg.Mode) && cfg.AllowOutbound
}

// wrapWithOutboundGate denies outbound integration tools (those whose
// [tools.Outbound] reports true) when the daemon runs in allow_all
// mode, the default, and the operator has not opted in with
// agent.approver.allow_outbound. Without it, a prompt-injected turn
// could read data and send it to a third party with no approval
// (L-32). Pattern / deny_all modes are returned unchanged: their rules
// already decide. The registry is consulted per call, so tools
// registered after the wrap (MCP, A2A) are classified too.
func wrapWithOutboundGate(inner agent.Approver, cfg config.ApproverConfig, reg *tools.Registry) agent.Approver {
	if reg == nil || !isAllowAllMode(cfg.Mode) || cfg.AllowOutbound {
		return inner
	}
	return &outboundGate{inner: inner, reg: reg}
}

type outboundGate struct {
	inner agent.Approver
	reg   *tools.Registry
}

// Approve satisfies agent.Approver.
func (g *outboundGate) Approve(ctx context.Context, req agent.ApprovalRequest) (agent.Decision, string) {
	if t, ok := g.reg.Get(req.ToolName); ok && tools.IsOutbound(t) {
		return agent.DecisionDeny, "outbound tool " + req.ToolName +
			" needs an approver rule: set agent.approver.allow_outbound: true or use pattern mode with an allow rule"
	}
	return g.inner.Approve(ctx, req)
}

// checkOutboundPosture renders the agent.approver.outbound doctor row:
// warn when allow_all runs outbound integration tools unasked, info
// otherwise.
func checkOutboundPosture(cfg *config.Config) []diagResult {
	ap := cfg.Agent.Approver
	row := diagResult{Name: "agent.approver.outbound", Status: "info"}
	switch {
	case outboundUngated(ap):
		row.Status = "warn"
		row.Detail = "allow_all with allow_outbound: true — outbound tools (email, chat posts, issue writes, Composio actions) run with no approval; a prompt-injected turn can send data out"
	case isAllowAllMode(ap.Mode):
		row.Detail = "allow_all denies outbound tools; set allow_outbound: true or use pattern mode with an allow rule to run them"
	default:
		row.Detail = "outbound tools are decided by the " + strings.ToLower(strings.TrimSpace(ap.Mode)) + " approver rules"
	}
	return []diagResult{row}
}
