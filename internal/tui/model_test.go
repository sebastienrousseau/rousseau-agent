package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

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
