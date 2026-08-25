package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
)

// --- fixtures ----------------------------------------------------------

// streamingStubRunner satisfies StreamingRunner so doTurn takes the
// incremental branch. It pushes every delta then closes the channel,
// exactly as the real agent loop does.
type streamingStubRunner struct {
	deltas []string
	reply  agent.Message
	err    error
}

func (s *streamingStubRunner) Turn(_ context.Context, sess *agent.Session) (agent.Message, error) {
	if s.err != nil {
		return agent.Message{}, s.err
	}
	sess.Append(s.reply)
	return s.reply, nil
}

func (s *streamingStubRunner) TurnStream(_ context.Context, sess *agent.Session, events chan<- agent.StreamEvent) (agent.Message, error) {
	defer close(events)
	events <- agent.StreamEvent{Kind: agent.StreamStart}
	for _, d := range s.deltas {
		events <- agent.StreamEvent{Kind: agent.StreamTextDelta, Delta: d}
	}
	if s.err != nil {
		return agent.Message{}, s.err
	}
	sess.Append(s.reply)
	return s.reply, nil
}

// failingStore is a state.Store whose Save always fails, so the TUI's
// "keep going, just warn" behaviour is observable.
type failingStore struct{ saveErr error }

func (f *failingStore) Save(context.Context, *agent.Session) error { return f.saveErr }
func (f *failingStore) Load(context.Context, string) (*agent.Session, error) {
	return nil, errors.New("not implemented")
}
func (f *failingStore) List(context.Context, int) ([]state.Summary, error) { return nil, nil }
func (f *failingStore) Delete(context.Context, string) error              { return nil }
func (f *failingStore) Close() error                                      { return nil }

var _ state.Store = (*failingStore)(nil)

func sizedModel(t *testing.T, runner Runner, store state.Store, sess *agent.Session) Model {
	t.Helper()
	m := New(runner, store, silentLogger(), sess)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return sized.(Model)
}

// --- stream pump -------------------------------------------------------

func TestUpdate_StreamPumpRendersDeltaAndReschedules(t *testing.T) {
	sess := agent.NewSession("t")
	sess.Append(agent.NewUserText("question"))
	m := sizedModel(t, &stubRunner{}, newTestStore(t), sess)

	events := make(chan agent.StreamEvent, 1)
	events <- agent.StreamEvent{Kind: agent.StreamTextDelta, Delta: "world"}
	close(events)

	updated, cmd := m.Update(streamPumpMsg{delta: "hello ", next: events})
	got := updated.(Model)

	assert.Equal(t, "hello ", got.streamBuf.String())
	assert.Contains(t, got.viewport.View(), "hello",
		"in-flight text must be previewed under the history")

	// The returned Cmd keeps pumping the same channel.
	require.NotNil(t, cmd)
	next, ok := cmd().(streamPumpMsg)
	require.True(t, ok)
	assert.Equal(t, "world", next.delta)
}

func TestUpdate_StreamPumpWithoutNextChannelStops(t *testing.T) {
	m := sizedModel(t, &stubRunner{}, newTestStore(t), agent.NewSession("t"))

	updated, cmd := m.Update(streamPumpMsg{delta: "tail"})
	tail := updated.(Model)
	assert.Equal(t, "tail", tail.streamBuf.String())
	assert.Nil(t, cmd, "no channel left to pump")
}

func TestUpdate_StreamPumpEmptyDeltaKeepsPumping(t *testing.T) {
	m := sizedModel(t, &stubRunner{}, newTestStore(t), agent.NewSession("t"))

	events := make(chan agent.StreamEvent)
	close(events)
	updated, cmd := m.Update(streamPumpMsg{next: events})

	pumped := updated.(Model)
	assert.Empty(t, pumped.streamBuf.String())
	require.NotNil(t, cmd)
	assert.Nil(t, cmd(), "closed channel ends the pump")
}

func TestUpdate_TurnResultClearsStreamBuffer(t *testing.T) {
	sess := agent.NewSession("t")
	sess.Append(agent.NewUserText("q"))
	m := sizedModel(t, &stubRunner{}, newTestStore(t), sess)
	m.busy = true
	m.streamBuf.WriteString("partial output")

	updated, _ := m.Update(turnResult{msg: agent.NewAssistantText("done")})
	got := updated.(Model)

	assert.False(t, got.busy)
	assert.Empty(t, got.streamBuf.String())
}

// --- persistence -------------------------------------------------------

func TestUpdate_SaveFailureIsLoggedButNotFatal(t *testing.T) {
	sess := agent.NewSession("t")
	sess.Append(agent.NewUserText("q"))
	m := sizedModel(t, &stubRunner{}, &failingStore{saveErr: errors.New("disk full")}, sess)
	m.busy = true

	updated, cmd := m.Update(turnResult{msg: agent.NewAssistantText("hi")})
	got := updated.(Model)

	assert.False(t, got.busy)
	assert.Nil(t, got.err, "a save failure is not surfaced as a turn error")
	assert.Nil(t, cmd)
}

// --- spinner + passthrough --------------------------------------------

func TestUpdate_SpinnerTickOnlyAnimatesWhileBusy(t *testing.T) {
	m := sizedModel(t, &stubRunner{}, newTestStore(t), agent.NewSession("t"))
	tick, ok := m.spinner.Tick().(spinner.TickMsg)
	require.True(t, ok)

	_, idleCmd := m.Update(tick)
	assert.Nil(t, idleCmd, "idle model must not schedule another frame")

	m.busy = true
	_, busyCmd := m.Update(tick)
	require.NotNil(t, busyCmd, "busy model keeps the spinner running")
}

func TestUpdate_UnhandledMessagesReachViewportAndTextarea(t *testing.T) {
	m := sizedModel(t, &stubRunner{}, newTestStore(t), agent.NewSession("t"))

	// A plain rune keypress is not one of the model's own bindings, so
	// it falls through to the child components and lands in the input.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	assert.Equal(t, "x", updated.(Model).textarea.Value())
}

// --- view --------------------------------------------------------------

func TestView_ShowsThinkingIndicatorWhileBusy(t *testing.T) {
	m := sizedModel(t, &stubRunner{}, newTestStore(t), agent.NewSession("t"))
	m.busy = true
	assert.Contains(t, m.View(), "thinking")
}

func TestRenderHistory_SkipsUnknownRoles(t *testing.T) {
	sess := agent.NewSession("t")
	sess.Append(agent.Message{
		Role:    agent.Role("system"),
		Content: []agent.Content{{Kind: agent.ContentText, Text: "hidden system text"}},
	})
	sess.Append(agent.NewUserText("visible"))

	out := renderHistory(sess, 80)
	assert.NotContains(t, out, "hidden system text")
	assert.Contains(t, out, "visible")
}

func TestStreamPreview_EmptyTextRendersNothing(t *testing.T) {
	assert.Empty(t, streamPreview(""))
	assert.Contains(t, streamPreview("partial"), "partial")
}

// --- doTurn streaming branch -------------------------------------------

func TestDoTurn_StreamingRunnerBatchesPumpAndFinalWait(t *testing.T) {
	sess := agent.NewSession("t")
	sess.Append(agent.NewUserText("q"))
	runner := &streamingStubRunner{
		deltas: []string{"he", "llo"},
		reply:  agent.NewAssistantText("hello"),
	}
	m := sizedModel(t, runner, newTestStore(t), sess)

	cmds, ok := m.doTurn()().(tea.BatchMsg)
	require.True(t, ok, "streaming doTurn must return a batch")
	require.Len(t, cmds, 2)

	// Drive the pump to completion, feeding each message back through
	// Update the way Bubble Tea would.
	model := tea.Model(m)
	msg := cmds[0]()
	for msg != nil {
		pump, isPump := msg.(streamPumpMsg)
		require.True(t, isPump)
		var cmd tea.Cmd
		model, cmd = model.Update(pump)
		if cmd == nil {
			break
		}
		msg = cmd()
	}
	streamed := model.(Model)
	assert.Equal(t, "hello", streamed.streamBuf.String())

	// The second batched Cmd delivers the terminal result.
	final, isResult := cmds[1]().(turnResult)
	require.True(t, isResult)
	require.NoError(t, final.err)
	assert.Equal(t, "hello", final.msg.Content[0].Text)
}

func TestDoTurn_StreamingRunnerPropagatesError(t *testing.T) {
	sess := agent.NewSession("t")
	sess.Append(agent.NewUserText("q"))
	boom := errors.New("provider exploded")
	m := sizedModel(t, &streamingStubRunner{err: boom}, newTestStore(t), sess)

	cmds, ok := m.doTurn()().(tea.BatchMsg)
	require.True(t, ok)
	require.Len(t, cmds, 2)

	done := make(chan turnResult, 1)
	go func() { done <- cmds[1]().(turnResult) }()

	select {
	case res := <-done:
		assert.ErrorIs(t, res.err, boom)
	case <-time.After(5 * time.Second):
		t.Fatal("finalWait never resolved")
	}
}

func TestFinalWait_ReturnsTheQueuedResult(t *testing.T) {
	ch := make(chan turnResult, 1)
	ch <- turnResult{msg: agent.NewAssistantText("queued")}
	msg, ok := finalWait(ch)().(turnResult)
	require.True(t, ok)
	assert.Equal(t, "queued", msg.msg.Content[0].Text)
}
