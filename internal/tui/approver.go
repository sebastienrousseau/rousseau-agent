package tui

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Approver is an agent.Approver that prompts the interactive TUI user
// for a per-tool-call decision.
//
// Wiring is a two-step dance because the tea.Program does not exist
// when the agent is being constructed:
//
//  1. NewApprover() returns an approver with no bound Send. Any
//     Approve() call before Bind() fail-closes (returns DecisionDeny
//     with a diagnostic reason) so a race window cannot silently
//     approve a tool call the operator never saw.
//  2. After tea.NewProgram(model) but before program.Run(), the
//     caller invokes Bind(program.Send). Send is safe to call from
//     any goroutine and is buffered by the tea runtime.
//
// The approver remembers per-session "always allow" / "always deny"
// decisions per tool name so a user who's already answered "always
// allow bash for this session" isn't prompted again. The memory is
// per-Approver (per-session), not persisted — a fresh chat starts
// with an empty policy.
//
// Approve is called on the agent goroutine; the TUI update loop runs
// on the tea goroutine. Communication happens via a fresh response
// channel per prompt, so multiple pending prompts (from concurrent
// sub-agents) each have their own channel. Cancellation flows through
// ctx: the agent aborts the turn → ctx cancels → Approve returns a
// deny with reason "cancelled".
type Approver struct {
	mu         sync.Mutex
	send       func(tea.Msg)
	remembered map[string]agent.Decision
}

// NewApprover constructs an unbound Approver. Call Bind before the
// agent starts consuming tools.
func NewApprover() *Approver {
	return &Approver{
		remembered: map[string]agent.Decision{},
	}
}

// Bind attaches the tea.Program's Send function. Safe to call
// exactly once from any goroutine. A subsequent Bind is a no-op —
// the first Send wins so tests can inject a fake and the wiring in
// chat.go cannot accidentally rebind mid-flight.
func (a *Approver) Bind(send func(tea.Msg)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.send == nil {
		a.send = send
	}
}

// Approve satisfies agent.Approver. Blocks the tool call until the
// TUI user answers or ctx is cancelled.
func (a *Approver) Approve(ctx context.Context, req agent.ApprovalRequest) (agent.Decision, string) {
	// Sticky-decision fast path — never re-prompts once the user
	// picked "always allow" or "always deny" for the tool.
	a.mu.Lock()
	if d, ok := a.remembered[req.ToolName]; ok {
		send := a.send
		a.mu.Unlock()
		if send != nil {
			send(approvalStickyMsg{tool: req.ToolName, decision: d})
		}
		if d == agent.DecisionDeny {
			return agent.DecisionDeny, "denied by session policy (always-deny)"
		}
		return agent.DecisionAllow, ""
	}
	send := a.send
	a.mu.Unlock()

	if send == nil {
		// Unbound → fail closed. Better than a silent allow of a tool
		// call the user never saw.
		return agent.DecisionDeny, "tui approver not bound"
	}

	respond := make(chan approvalResponse, 1)
	send(approvalRequestedMsg{req: req, respond: respond, summary: summarise(req)})

	select {
	case <-ctx.Done():
		return agent.DecisionDeny, "approval cancelled"
	case resp := <-respond:
		if resp.remember {
			a.mu.Lock()
			a.remembered[req.ToolName] = resp.decision
			a.mu.Unlock()
		}
		if resp.decision == agent.DecisionDeny {
			reason := resp.reason
			if reason == "" {
				reason = "denied by user"
			}
			return agent.DecisionDeny, reason
		}
		return agent.DecisionAllow, ""
	}
}

// remembers reports whether the approver has a sticky decision on
// file for the tool. Used only by tests.
func (a *Approver) remembers(tool string) (agent.Decision, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d, ok := a.remembered[tool]
	return d, ok
}

// approvalRequestedMsg is delivered to the TUI Update loop when a
// tool call is waiting on the user. respond is a per-prompt channel
// the Update handler writes exactly one response to.
type approvalRequestedMsg struct {
	req     agent.ApprovalRequest
	summary string
	respond chan<- approvalResponse
}

// approvalStickyMsg tells the TUI that a call was auto-decided by a
// remembered policy (skipping the prompt). Rendered as a one-line
// info note so the user knows the sticky decision is still active.
type approvalStickyMsg struct {
	tool     string
	decision agent.Decision
}

// approvalResponse is the reply from the TUI to the agent.
type approvalResponse struct {
	decision agent.Decision
	reason   string
	remember bool
}

// summarise returns a compact one-line description of a pending
// approval — the tool name plus the single most-useful field from
// the JSON input. Mirrors internal/agent/tool_summary.go's rules but
// stays a local copy so this package does not import from agent (it
// already does for the interface; adding a summary helper import
// would grow the surface further).
func summarise(req agent.ApprovalRequest) string {
	if len(req.Input) == 0 {
		return req.ToolName
	}
	var m map[string]any
	if err := json.Unmarshal(req.Input, &m); err != nil || len(m) == 0 {
		return req.ToolName
	}
	var detail string
	switch strings.ToLower(req.ToolName) {
	case "read", "write", "edit":
		detail = stringField(m, "file_path", "path")
	case "bash":
		detail = firstLine(stringField(m, "command"))
	case "grep":
		detail = stringField(m, "pattern", "path")
	case "webfetch", "web_fetch":
		detail = stringField(m, "url")
	default:
		detail = stringField(m, "path", "url", "command", "query", "pattern", "description")
	}
	if detail = trimSummary(detail); detail == "" {
		return req.ToolName
	}
	return req.ToolName + " " + detail
}

// stringField returns the first non-empty string value in m among keys.
func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// firstLine trims s to its first line.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// trimSummary caps s at 60 runes with a single "…" tail. Rune-safe.
func trimSummary(s string) string {
	const maxRunes = 60
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}
