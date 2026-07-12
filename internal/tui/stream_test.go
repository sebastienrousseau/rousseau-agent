package tui

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

type streamingStubRunner struct {
	deltas []string
	reply  string
	err    error
}

func (s *streamingStubRunner) Turn(context.Context, *agent.Session) (agent.Message, error) {
	return agent.NewAssistantText(s.reply), s.err
}

func (s *streamingStubRunner) TurnStream(_ context.Context, sess *agent.Session, events chan<- agent.StreamEvent) (agent.Message, error) {
	for _, d := range s.deltas {
		events <- agent.StreamEvent{Kind: agent.StreamTextDelta, Delta: d}
	}
	close(events)
	msg := agent.NewAssistantText(s.reply)
	sess.Append(msg)
	return msg, s.err
}

func TestDeltaPump_ReceivesEvent(t *testing.T) {
	events := make(chan agent.StreamEvent, 2)
	events <- agent.StreamEvent{Kind: agent.StreamTextDelta, Delta: "hi"}
	close(events)
	cmd := deltaPump(events)
	msg := cmd()
	pump, ok := msg.(streamPumpMsg)
	require.True(t, ok)
	assert.Equal(t, "hi", pump.delta)
}

func TestDeltaPump_ClosedChannelReturnsNil(t *testing.T) {
	events := make(chan agent.StreamEvent)
	close(events)
	cmd := deltaPump(events)
	assert.Nil(t, cmd())
}

func TestDeltaPump_NonTextEventPassesThroughEmpty(t *testing.T) {
	events := make(chan agent.StreamEvent, 1)
	events <- agent.StreamEvent{Kind: agent.StreamStart}
	close(events)
	cmd := deltaPump(events)
	msg := cmd()
	pump, ok := msg.(streamPumpMsg)
	require.True(t, ok)
	assert.Empty(t, pump.delta)
}

func TestStreamPreview_EmptyReturnsEmpty(t *testing.T) {
	assert.Empty(t, streamPreview(""))
}

func TestStreamPreview_WrapsText(t *testing.T) {
	got := streamPreview("some content")
	assert.Contains(t, got, "some content")
	assert.Contains(t, got, "rousseau")
}

func TestFinalWait_ReceivesResult(t *testing.T) {
	result := make(chan turnResult, 1)
	result <- turnResult{msg: agent.NewAssistantText("done")}
	msg := finalWait(result)()
	tr, ok := msg.(turnResult)
	require.True(t, ok)
	assert.Equal(t, "done", tr.msg.Content[0].Text)
}
