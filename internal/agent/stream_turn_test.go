package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// streamingStub implements StreamingProvider for tests.
type streamingStub struct {
	deltas   []string
	final    Message
	stopWith StopReason
	err      error
}

func (s *streamingStub) Name() string { return "stream-stub" }

func (s *streamingStub) Complete(context.Context, Request) (Response, error) {
	return Response{Message: s.final, StopReason: s.stopWith}, s.err
}

func (s *streamingStub) Stream(_ context.Context, _ Request) (<-chan StreamEvent, <-chan StreamReport, error) {
	events := make(chan StreamEvent, len(s.deltas)+2)
	report := make(chan StreamReport, 1)
	go func() {
		defer close(events)
		defer close(report)
		events <- StreamEvent{Kind: StreamStart}
		for _, d := range s.deltas {
			events <- StreamEvent{Kind: StreamTextDelta, Delta: d}
		}
		events <- StreamEvent{Kind: StreamResult}
		report <- StreamReport{
			Response: Response{Message: s.final, StopReason: s.stopWith},
			Err:      s.err,
		}
	}()
	return events, report, nil
}

func streamSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTurnStream_EmitsDeltasAndReturnsMessage(t *testing.T) {
	stub := &streamingStub{
		deltas: []string{"hel", "lo"},
		final: Message{Role: RoleAssistant, Content: []Content{
			{Kind: ContentText, Text: "hello"},
		}},
		stopWith: StopEndTurn,
	}
	a := New(stub, tools.NewRegistry(), streamSilentLogger(), Options{})
	s := NewSession("x")
	s.Append(NewUserText("hi"))

	events := make(chan StreamEvent, 8)

	var collected []string
	done := make(chan struct{})
	go func() {
		for e := range events {
			if e.Kind == StreamTextDelta {
				collected = append(collected, e.Delta)
			}
		}
		close(done)
	}()

	final, err := a.TurnStream(context.Background(), s, events)
	require.NoError(t, err)
	<-done

	require.Len(t, final.Content, 1)
	assert.Equal(t, "hello", final.Content[0].Text)
	assert.Equal(t, []string{"hel", "lo"}, collected)
}

func TestTurnStream_EmptySession(t *testing.T) {
	stub := &streamingStub{}
	a := New(stub, tools.NewRegistry(), streamSilentLogger(), Options{})
	events := make(chan StreamEvent, 4)
	_, err := a.TurnStream(context.Background(), NewSession("x"), events)
	assert.ErrorIs(t, err, ErrEmptySession)
}

func TestTurnStream_ProviderError(t *testing.T) {
	stub := &streamingStub{
		err:      errors.New("boom"),
		final:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: ""}}},
		stopWith: StopEndTurn,
	}
	a := New(stub, tools.NewRegistry(), streamSilentLogger(), Options{})
	s := NewSession("x")
	s.Append(NewUserText("go"))
	events := make(chan StreamEvent, 8)
	// Drain in the background so TurnStream can send.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range events {
		}
	}()

	_, err := a.TurnStream(context.Background(), s, events)
	<-drained
	assert.Error(t, err)
}

func TestTurnStream_FallsBackToCompleteWhenProviderIsNotStreaming(t *testing.T) {
	nonStreaming := &stubProvider{
		responses: []Response{
			{
				Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "hi"}}},
				StopReason: StopEndTurn,
			},
		},
	}
	a := New(nonStreaming, tools.NewRegistry(), streamSilentLogger(), Options{})
	s := NewSession("x")
	s.Append(NewUserText("hello"))
	events := make(chan StreamEvent, 4)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range events {
		}
	}()

	final, err := a.TurnStream(context.Background(), s, events)
	<-drained
	require.NoError(t, err)
	assert.Equal(t, "hi", final.Content[0].Text)
}
