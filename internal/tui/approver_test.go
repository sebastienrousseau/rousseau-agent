package tui

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// captureSend collects every tea.Msg the approver sends. Used to
// assert message shape and to satisfy Bind without a real program.
type captureSend struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (c *captureSend) send(m tea.Msg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
}

func (c *captureSend) snapshot() []tea.Msg {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]tea.Msg, len(c.msgs))
	copy(out, c.msgs)
	return out
}

func TestApprover_UnboundFailsClosed(t *testing.T) {
	// If Approve is called before Bind, the approver MUST NOT silently
	// allow — the user has not seen the request, so allowing it would
	// bypass the entire feature. Deny is the safe disposition.
	a := NewApprover()
	d, reason := a.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, d)
	assert.Contains(t, reason, "not bound")
}

func TestApprover_AllowResponse(t *testing.T) {
	a := NewApprover()
	cap := &captureSend{}
	a.Bind(cap.send)

	// Approve runs on the "agent goroutine" — spawn it and answer via
	// the response channel the message carries.
	got := runInBackground(t, func() (agent.Decision, string) {
		return a.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	})

	msg := waitForMsg[approvalRequestedMsg](t, cap, 500*time.Millisecond)
	msg.respond <- approvalResponse{decision: agent.DecisionAllow}

	res := <-got
	assert.Equal(t, agent.DecisionAllow, res.decision)
	assert.Empty(t, res.reason)
	// Not sticky — the approver must not remember a plain allow.
	_, ok := a.remembers("read")
	assert.False(t, ok)
}

func TestApprover_DenyResponsePropagatesReason(t *testing.T) {
	a := NewApprover()
	cap := &captureSend{}
	a.Bind(cap.send)

	got := runInBackground(t, func() (agent.Decision, string) {
		return a.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	})

	msg := waitForMsg[approvalRequestedMsg](t, cap, 500*time.Millisecond)
	msg.respond <- approvalResponse{decision: agent.DecisionDeny}

	res := <-got
	assert.Equal(t, agent.DecisionDeny, res.decision)
	assert.Equal(t, "denied by user", res.reason, "empty reason falls back to a legible default")
}

func TestApprover_RememberedDecisionShortCircuitsFuturePrompts(t *testing.T) {
	// First approve: user picks "always allow". Second approve for the
	// same tool must NOT re-prompt (no new approvalRequestedMsg) — it
	// should return DecisionAllow immediately AND emit a
	// approvalStickyMsg so the TUI can show a note.
	a := NewApprover()
	cap := &captureSend{}
	a.Bind(cap.send)

	// First prompt: remember allow.
	got := runInBackground(t, func() (agent.Decision, string) {
		return a.Approve(context.Background(), agent.ApprovalRequest{ToolName: "grep"})
	})
	msg := waitForMsg[approvalRequestedMsg](t, cap, 500*time.Millisecond)
	msg.respond <- approvalResponse{decision: agent.DecisionAllow, remember: true}
	res := <-got
	require.Equal(t, agent.DecisionAllow, res.decision)
	d, ok := a.remembers("grep")
	require.True(t, ok)
	assert.Equal(t, agent.DecisionAllow, d)

	// Second call: no new prompt, sticky note fires.
	before := len(cap.snapshot())
	d2, reason := a.Approve(context.Background(), agent.ApprovalRequest{ToolName: "grep"})
	assert.Equal(t, agent.DecisionAllow, d2)
	assert.Empty(t, reason)
	after := cap.snapshot()
	require.Len(t, after, before+1, "exactly one new msg (the sticky note), no fresh prompt")
	sticky, ok := after[len(after)-1].(approvalStickyMsg)
	require.True(t, ok, "the new msg must be a sticky note, not a fresh prompt")
	assert.Equal(t, "grep", sticky.tool)
	assert.Equal(t, agent.DecisionAllow, sticky.decision)
}

func TestApprover_RememberedDenyProducesDenyReason(t *testing.T) {
	// Sticky-deny: the reason carries the "session policy" label so
	// the model can distinguish an in-session policy denial from a
	// fresh user deny.
	a := NewApprover()
	cap := &captureSend{}
	a.Bind(cap.send)

	got := runInBackground(t, func() (agent.Decision, string) {
		return a.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	})
	msg := waitForMsg[approvalRequestedMsg](t, cap, 500*time.Millisecond)
	msg.respond <- approvalResponse{decision: agent.DecisionDeny, remember: true, reason: "test"}
	<-got // discard first

	d, reason := a.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, d)
	assert.Contains(t, reason, "always-deny")
}

func TestApprover_CtxCancellationDenies(t *testing.T) {
	// If the turn's context is cancelled while an approval is
	// pending, the approver must unblock with a deny — otherwise the
	// tool call hangs forever waiting on a user who's gone.
	a := NewApprover()
	cap := &captureSend{}
	a.Bind(cap.send)

	ctx, cancel := context.WithCancel(context.Background())
	got := runInBackground(t, func() (agent.Decision, string) {
		return a.Approve(ctx, agent.ApprovalRequest{ToolName: "bash"})
	})
	_ = waitForMsg[approvalRequestedMsg](t, cap, 500*time.Millisecond)
	cancel()

	res := <-got
	assert.Equal(t, agent.DecisionDeny, res.decision)
	assert.Contains(t, res.reason, "cancelled")
}

func TestApprover_MessageCarriesToolSummary(t *testing.T) {
	// The summary field on approvalRequestedMsg is what the TUI
	// renders to the user. It must be legible per tool convention —
	// "read foo.go" not just "read".
	a := NewApprover()
	cap := &captureSend{}
	a.Bind(cap.send)

	go func() {
		_, _ = a.Approve(context.Background(), agent.ApprovalRequest{
			ToolName: "Read",
			Input:    json.RawMessage(`{"file_path":"internal/foo.go"}`),
		})
	}()
	msg := waitForMsg[approvalRequestedMsg](t, cap, 500*time.Millisecond)
	assert.Equal(t, "Read internal/foo.go", msg.summary)
	msg.respond <- approvalResponse{decision: agent.DecisionAllow}
}

func TestSummarise(t *testing.T) {
	tests := []struct {
		name string
		req  agent.ApprovalRequest
		want string
	}{
		{"empty input keeps tool name", agent.ApprovalRequest{ToolName: "read"}, "read"},
		{"non-JSON keeps tool name", agent.ApprovalRequest{ToolName: "read", Input: json.RawMessage(`x`)}, "read"},
		{"Read file_path", agent.ApprovalRequest{ToolName: "Read", Input: json.RawMessage(`{"file_path":"a.go"}`)}, "Read a.go"},
		{"Bash first line only", agent.ApprovalRequest{ToolName: "bash", Input: json.RawMessage(`{"command":"go test\nrm -rf /"}`)}, "bash go test"},
		{"Grep pattern", agent.ApprovalRequest{ToolName: "Grep", Input: json.RawMessage(`{"pattern":"foo"}`)}, "Grep foo"},
		{"unknown tool falls back to common fields", agent.ApprovalRequest{ToolName: "WeirdTool", Input: json.RawMessage(`{"query":"hi"}`)}, "WeirdTool hi"},
		{"long summary is truncated with ellipsis", agent.ApprovalRequest{ToolName: "Read", Input: json.RawMessage(`{"file_path":"` + strings.Repeat("x", 80) + `"}`)}, "Read " + strings.Repeat("x", 59) + "…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, summarise(tc.req))
		})
	}
}

func TestBind_IsIdempotent(t *testing.T) {
	// A second Bind must not overwrite the first — tests inject a
	// capture, then the real chat.go Bind wouldn't accidentally
	// steal the wire.
	a := NewApprover()
	first := &captureSend{}
	second := &captureSend{}
	a.Bind(first.send)
	a.Bind(second.send)

	go func() {
		_, _ = a.Approve(context.Background(), agent.ApprovalRequest{ToolName: "x"})
	}()
	msg := waitForMsg[approvalRequestedMsg](t, first, 500*time.Millisecond)
	msg.respond <- approvalResponse{decision: agent.DecisionAllow}

	assert.Empty(t, second.snapshot(), "second Bind must have been ignored")
}

// helpers -----------------------------------------------------------

type asyncResult struct {
	decision agent.Decision
	reason   string
}

func runInBackground(t *testing.T, fn func() (agent.Decision, string)) <-chan asyncResult {
	t.Helper()
	out := make(chan asyncResult, 1)
	go func() {
		d, r := fn()
		out <- asyncResult{decision: d, reason: r}
	}()
	return out
}

// waitForMsg[T] polls cap.snapshot() until a msg of type T appears
// (returning the LAST one) or the deadline elapses.
func waitForMsg[T tea.Msg](t *testing.T, cap *captureSend, timeout time.Duration) T {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, m := range cap.snapshot() {
			if v, ok := m.(T); ok {
				return v
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	var zero T
	t.Fatalf("timed out waiting for %T", zero)
	return zero
}
