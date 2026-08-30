package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

type stubRunner struct {
	reply agent.Message
	err   error
}

func (s *stubRunner) Turn(_ context.Context, sess *agent.Session) (agent.Message, error) {
	if s.err != nil {
		return agent.Message{}, s.err
	}
	sess.Append(s.reply)
	return s.reply, nil
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestStore(t *testing.T) *sqlitestore.Store {
	t.Helper()
	store, err := sqlitestore.Open(context.Background(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup
	return store
}

func TestNew_InitReturnsCommand(t *testing.T) {
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	cmd := m.Init()
	assert.NotNil(t, cmd)
}

func TestView_RendersHeaderAndTextarea(t *testing.T) {
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("hello"))
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := updated.(Model).View()
	assert.Contains(t, view, "rousseau")
	assert.Contains(t, view, "hello")
}

func TestUpdate_WindowSizeAdjustsViewport(t *testing.T) {
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	got := updated.(Model)
	assert.Equal(t, 120, got.viewport.Width)
	assert.Equal(t, 120, got.width)
}

func TestUpdate_CtrlCQuits(t *testing.T) {
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	assert.True(t, isQuit)
}

func TestUpdate_TurnResultSaves(t *testing.T) {
	sess := agent.NewSession("t")
	sess.Append(agent.NewUserText("hello"))
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), sess)
	reply := agent.NewAssistantText("hi")
	updated, _ := m.Update(turnResult{msg: reply})
	got := updated.(Model)
	assert.False(t, got.busy)
	assert.Nil(t, got.err)
}

func TestUpdate_TurnResultRecordsError(t *testing.T) {
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	updated, _ := m.Update(turnResult{err: errors.New("boom")})
	got := updated.(Model)
	assert.Error(t, got.err)
}

func TestRenderHistory_AllContentKinds(t *testing.T) {
	sess := agent.NewSession("t")
	sess.Append(agent.NewUserText("hi"))
	sess.Append(agent.Message{
		Role: agent.RoleAssistant,
		Content: []agent.Content{
			{Kind: agent.ContentText, Text: "let me look"},
			{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{
				ID: "1", Name: "read", Input: json.RawMessage(`{"path": "/x"}`),
			}},
		},
	})
	sess.Append(agent.Message{
		Role: agent.RoleUser,
		Content: []agent.Content{
			{Kind: agent.ContentToolResult, ToolResult: &agent.ToolResult{
				ToolUseID: "1", Output: "some contents",
			}},
			{Kind: agent.ContentToolResult, ToolResult: &agent.ToolResult{
				ToolUseID: "2", Output: "err", IsError: true,
			}},
		},
	})
	out := renderHistory(sess, 80)
	assert.Contains(t, out, "hi")
	assert.Contains(t, out, "read")
	assert.Contains(t, out, "some contents")
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hi", truncate("hi", 10))
	assert.Equal(t, "hell…", truncate("hello world", 4))
}

func TestMaxInt(t *testing.T) {
	assert.Equal(t, 5, maxInt(5, 3))
	assert.Equal(t, 5, maxInt(3, 5))
}

func TestUpdate_EnterEmptyIsNoop(t *testing.T) {
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	assert.False(t, got.busy)
	_ = cmd
}

func TestUpdate_EnterSubmitsAndStartsBusy(t *testing.T) {
	sess := agent.NewSession("t")
	m := New(&stubRunner{reply: agent.NewAssistantText("ok")}, newTestStore(t), silentLogger(), sess)
	// Simulate size then typing.
	m0, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = m0.(Model)
	m.textarea.SetValue("hello")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	assert.True(t, got.busy)
	require.NotNil(t, cmd)
}

func TestUpdate_EnterWhileBusyIsNoop(t *testing.T) {
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	m.busy = true
	m.textarea.SetValue("hi")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	assert.True(t, got.busy)
	assert.Equal(t, "hi", got.textarea.Value())
}

func TestDoTurn_ProducesResult(t *testing.T) {
	sess := agent.NewSession("t")
	sess.Append(agent.NewUserText("q"))
	m := New(&stubRunner{reply: agent.NewAssistantText("a")}, newTestStore(t), silentLogger(), sess)
	cmd := m.doTurn()
	require.NotNil(t, cmd)
	msg := cmd()
	res, ok := msg.(turnResult)
	require.True(t, ok)
	assert.NoError(t, res.err)
}

func TestView_ShowsErrorWhenSet(t *testing.T) {
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	m0, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = m0.(Model)
	m.err = errors.New("boom")
	view := m.View()
	assert.Contains(t, view, "boom")
}

// buildPendingModel returns a Model with a single approvalRequestedMsg
// staged as if the approver had just fired. Used to exercise the
// modal-keyboard branches of Update without wiring a live Approver +
// program.
func buildPendingModel(t *testing.T, tool string) (Model, chan approvalResponse) {
	t.Helper()
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	respond := make(chan approvalResponse, 1)
	updated, _ := m.Update(approvalRequestedMsg{
		req:     agent.ApprovalRequest{ToolName: tool, Input: json.RawMessage(`{"file_path":"x.go"}`)},
		summary: tool + " x.go",
		respond: respond,
	})
	return updated.(Model), respond
}

func TestUpdate_ApprovalPromptYAllows(t *testing.T) {
	m, respond := buildPendingModel(t, "read")
	require.NotNil(t, m.pending)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	got := updated.(Model)
	assert.Nil(t, got.pending, "y clears the pending prompt")

	select {
	case r := <-respond:
		assert.Equal(t, agent.DecisionAllow, r.decision)
		assert.False(t, r.remember, "y is one-shot, not sticky")
	default:
		t.Fatal("no response sent to the approver")
	}
}

func TestUpdate_ApprovalPromptNDenies(t *testing.T) {
	m, respond := buildPendingModel(t, "bash")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	got := updated.(Model)
	assert.Nil(t, got.pending)

	r := <-respond
	assert.Equal(t, agent.DecisionDeny, r.decision)
	assert.Equal(t, "denied by user", r.reason)
	assert.False(t, r.remember)
}

func TestUpdate_ApprovalPromptARemembersAllow(t *testing.T) {
	m, respond := buildPendingModel(t, "grep")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	got := updated.(Model)
	assert.Nil(t, got.pending)
	assert.Contains(t, got.stickyNote, "always allowing")
	assert.Contains(t, got.stickyNote, "grep")

	r := <-respond
	assert.Equal(t, agent.DecisionAllow, r.decision)
	assert.True(t, r.remember, "[a] must set remember so subsequent grep calls skip the prompt")
}

func TestUpdate_ApprovalPromptDRemembersDeny(t *testing.T) {
	m, respond := buildPendingModel(t, "bash")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	got := updated.(Model)
	assert.Nil(t, got.pending)
	assert.Contains(t, got.stickyNote, "always denying")

	r := <-respond
	assert.Equal(t, agent.DecisionDeny, r.decision)
	assert.True(t, r.remember)
}

func TestUpdate_ApprovalPromptCtrlCDeniesAndQuits(t *testing.T) {
	m, respond := buildPendingModel(t, "read")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := updated.(Model)
	assert.Nil(t, got.pending)
	require.NotNil(t, cmd, "ctrl+c during approval must still emit tea.Quit")

	r := <-respond
	assert.Equal(t, agent.DecisionDeny, r.decision)
	assert.Contains(t, r.reason, "quit")
}

func TestUpdate_ApprovalPromptOtherKeysSwallowed(t *testing.T) {
	// Any key that isn't y/n/a/d/ctrl+c while pending is dropped —
	// no textarea input, no viewport scroll. This is the modal
	// guarantee.
	m, respond := buildPendingModel(t, "read")
	before := m.textarea.Value()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	got := updated.(Model)
	assert.NotNil(t, got.pending, "prompt still active after non-matching key")
	assert.Equal(t, before, got.textarea.Value(), "textarea must not receive the key")
	assert.Nil(t, cmd)

	select {
	case r := <-respond:
		t.Fatalf("no response should have been sent; got %+v", r)
	default:
	}
}

func TestView_RendersApprovalPrompt(t *testing.T) {
	// Approval mode replaces the status footer with a two-line
	// modal (label + hotkey hint).
	m, _ := buildPendingModel(t, "read")
	view := m.View()
	assert.Contains(t, view, "approve")
	assert.Contains(t, view, "[y]es")
	assert.Contains(t, view, "[a]lways allow")
}

func TestView_RendersStickyNoteWhenSet(t *testing.T) {
	// After an [a]/[d] on the previous prompt, or after an
	// approvalStickyMsg auto-decision, the sticky note appears as
	// the status footer.
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	m.stickyNote = "auto-allowed `bash` (session policy)"
	view := m.View()
	assert.Contains(t, view, "auto-allowed")
}

func TestUpdate_ApprovalStickyMsgUpdatesNote(t *testing.T) {
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	updated, _ := m.Update(approvalStickyMsg{tool: "bash", decision: agent.DecisionAllow})
	got := updated.(Model)
	assert.Contains(t, got.stickyNote, "auto-allowed")
	assert.Contains(t, got.stickyNote, "bash")

	updated, _ = m.Update(approvalStickyMsg{tool: "write", decision: agent.DecisionDeny})
	got = updated.(Model)
	assert.Contains(t, got.stickyNote, "auto-denied")
}

func TestUpdate_StreamPumpMsgAccumulates(t *testing.T) {
	sess := agent.NewSession("t")
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), sess)
	m0, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = m0.(Model)

	events := make(chan agent.StreamEvent, 4)
	updated, cmd := m.Update(streamPumpMsg{delta: "hel", next: events})
	got := updated.(Model)
	assert.Equal(t, "hel", got.streamBuf.String())
	require.NotNil(t, cmd, "with a non-nil next channel, Update must schedule the follow-up pump")
	close(events)
}

func TestUpdate_StreamPumpMsgWithoutDeltaStillReschedules(t *testing.T) {
	// Non-text stream events (tool_use etc.) come through with empty
	// delta. The buffer should not grow but the pump must continue.
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	events := make(chan agent.StreamEvent, 4)
	before := m.streamBuf.String()

	updated, cmd := m.Update(streamPumpMsg{delta: "", next: events})
	got := updated.(Model)
	assert.Equal(t, before, got.streamBuf.String())
	require.NotNil(t, cmd)
	close(events)
}

func TestUpdate_StreamPumpNilNextEndsPump(t *testing.T) {
	// A nil next signals end-of-stream — no follow-up Cmd should be
	// scheduled.
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	updated, cmd := m.Update(streamPumpMsg{delta: "last", next: nil})
	got := updated.(Model)
	assert.Equal(t, "last", got.streamBuf.String())
	assert.Nil(t, cmd)
}

func TestUpdate_SpinnerTickWhileIdleNoop(t *testing.T) {
	// A spinner tick that arrives when the model isn't busy must not
	// keep spinning — otherwise the animation runs forever after a
	// turn completes.
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	m.busy = false
	_, cmd := m.Update(spinner.TickMsg{})
	assert.Nil(t, cmd)
}

func TestUpdate_SpinnerTickWhileBusyReschedules(t *testing.T) {
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	m.busy = true
	_, cmd := m.Update(spinner.TickMsg{})
	assert.NotNil(t, cmd, "spinner must keep ticking while a turn is in flight")
}

func TestUpdate_UnhandledMsgFallsThroughToChildren(t *testing.T) {
	// Any msg that isn't recognised in the switch falls through and
	// updates the viewport + textarea. Sanity check that this
	// doesn't panic and returns a Batch Cmd.
	m := New(&stubRunner{}, newTestStore(t), silentLogger(), agent.NewSession("t"))
	updated, _ := m.Update("some unknown message")
	_ = updated.(Model)
}
