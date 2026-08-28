package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// streamStep is one provider round-trip: the events it emits followed
// by the terminal report. A nil report models a provider that closes
// the report channel without ever publishing an outcome.
type streamStep struct {
	events []StreamEvent
	report *StreamReport
}

// scriptedStreamer is a StreamingProvider driven by a fixed script.
type scriptedStreamer struct {
	steps     []streamStep
	streamErr error
	i         int
	// beforeSend is invoked (if set) just before the first event of a
	// step is pushed, so tests can race cancellation against delivery.
	beforeSend func()
}

func (s *scriptedStreamer) Name() string { return "scripted-stream" }

func (s *scriptedStreamer) Complete(context.Context, Request) (Response, error) {
	return Response{Message: NewAssistantText("non-streaming"), StopReason: StopEndTurn}, nil
}

func (s *scriptedStreamer) Stream(_ context.Context, _ Request) (<-chan StreamEvent, <-chan StreamReport, error) {
	if s.streamErr != nil {
		return nil, nil, s.streamErr
	}
	if s.i >= len(s.steps) {
		return nil, nil, errors.New("scripted streamer exhausted")
	}
	step := s.steps[s.i]
	s.i++

	events := make(chan StreamEvent, len(step.events))
	report := make(chan StreamReport, 1)
	go func() {
		defer close(events)
		defer close(report)
		if s.beforeSend != nil {
			s.beforeSend()
		}
		for _, e := range step.events {
			events <- e
		}
		if step.report != nil {
			report <- *step.report
		}
	}()
	return events, report, nil
}

func endTurnStep(text string) streamStep {
	return streamStep{
		events: []StreamEvent{{Kind: StreamTextDelta, Delta: text}},
		report: &StreamReport{
			Response: Response{Message: NewAssistantText(text), StopReason: StopEndTurn},
		},
	}
}

func toolUseStep(id, name, input string) streamStep {
	return streamStep{
		events: []StreamEvent{{Kind: StreamToolUse}},
		report: &StreamReport{Response: toolUseResponse(id, name, input)},
	}
}

// drain consumes the TurnStream event channel in the background and
// returns a func that yields every event once the turn finishes.
func drain(events <-chan StreamEvent) func() []StreamEvent {
	done := make(chan []StreamEvent, 1)
	go func() {
		var got []StreamEvent
		for e := range events {
			got = append(got, e)
		}
		done <- got
	}()
	return func() []StreamEvent { return <-done }
}

func TestTurnStream_RunsToolsBetweenStreamedRoundTrips(t *testing.T) {
	tool := &recordingTool{name: "echo", out: "pong"}
	prov := &scriptedStreamer{steps: []streamStep{
		toolUseStep("c1", "echo", `{"n":1}`),
		endTurnStep("all done"),
	}}
	a := New(prov, registryWith(t, tool), streamSilentLogger(), Options{})

	events := make(chan StreamEvent, 16)
	collect := drain(events)
	s := sessionWith("go")

	final, err := a.TurnStream(context.Background(), s, events)
	require.NoError(t, err)
	assert.Equal(t, "all done", final.Content[0].Text)
	assert.Equal(t, []string{`{"n":1}`}, tool.got)

	// user, assistant(tool_use), user(tool_result), assistant(text)
	require.Len(t, s.Messages, 4)
	assert.Equal(t, ContentToolResult, s.Messages[2].Content[0].Kind)
	assert.Equal(t, "pong", s.Messages[2].Content[0].ToolResult.Output)

	got := collect()
	assert.NotEmpty(t, got, "caller must observe the provider's events")
}

func TestTurnStream_ToolErrorIsReportedBackToTheModel(t *testing.T) {
	tool := &recordingTool{name: "flaky", err: errors.New("upstream timeout")}
	prov := &scriptedStreamer{steps: []streamStep{
		toolUseStep("c1", "flaky", `{}`),
		endTurnStep("noted"),
	}}
	a := New(prov, registryWith(t, tool), streamSilentLogger(), Options{})

	events := make(chan StreamEvent, 16)
	collect := drain(events)
	s := sessionWith("go")

	_, err := a.TurnStream(context.Background(), s, events)
	require.NoError(t, err, "a failing tool must not abort the turn")

	res := s.Messages[2].Content[0].ToolResult
	assert.True(t, res.IsError)
	assert.Equal(t, "upstream timeout", res.Output)
	collect()
}

func TestTurnStream_ToolNotFoundAborts(t *testing.T) {
	prov := &scriptedStreamer{steps: []streamStep{toolUseStep("c1", "nope", `{}`)}}
	a := New(prov, tools.NewRegistry(), streamSilentLogger(), Options{})

	events := make(chan StreamEvent, 16)
	collect := drain(events)

	_, err := a.TurnStream(context.Background(), sessionWith("go"), events)
	assert.ErrorIs(t, err, ErrToolNotFound)
	collect()
}

func TestTurnStream_MaxIterationsExhausted(t *testing.T) {
	tool := &recordingTool{name: "echo", out: ""}
	prov := &scriptedStreamer{steps: []streamStep{
		toolUseStep("c1", "echo", `{}`),
		toolUseStep("c2", "echo", `{}`),
		toolUseStep("c3", "echo", `{}`),
	}}
	a := New(prov, registryWith(t, tool), streamSilentLogger(), Options{MaxIterations: 2})

	events := make(chan StreamEvent, 32)
	collect := drain(events)

	_, err := a.TurnStream(context.Background(), sessionWith("go"), events)
	assert.ErrorIs(t, err, ErrMaxIterations)
	collect()
}

func TestTurnStream_StreamSetupErrorIsWrapped(t *testing.T) {
	boom := errors.New("stream unavailable")
	a := New(&scriptedStreamer{streamErr: boom}, tools.NewRegistry(), streamSilentLogger(), Options{})

	events := make(chan StreamEvent, 4)
	collect := drain(events)

	_, err := a.TurnStream(context.Background(), sessionWith("go"), events)
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "provider:")
	collect()
}

func TestTurnStream_ReportChannelClosedWithoutReport(t *testing.T) {
	prov := &scriptedStreamer{steps: []streamStep{{
		events: []StreamEvent{{Kind: StreamStart}},
		report: nil, // closed without publishing
	}}}
	a := New(prov, tools.NewRegistry(), streamSilentLogger(), Options{})

	events := make(chan StreamEvent, 4)
	collect := drain(events)

	_, err := a.TurnStream(context.Background(), sessionWith("go"), events)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed report channel")
	collect()
}

func TestTurnStream_ReportCarriedErrorSurfaces(t *testing.T) {
	boom := errors.New("model overloaded")
	prov := &scriptedStreamer{steps: []streamStep{{
		events: []StreamEvent{{Kind: StreamStart}},
		report: &StreamReport{Err: boom},
	}}}
	a := New(prov, tools.NewRegistry(), streamSilentLogger(), Options{})

	events := make(chan StreamEvent, 4)
	collect := drain(events)

	_, err := a.TurnStream(context.Background(), sessionWith("go"), events)
	assert.ErrorIs(t, err, boom)
	collect()
}

func TestTurnStream_CancelledContextStopsForwarding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	prov := &scriptedStreamer{
		steps:      []streamStep{{events: []StreamEvent{{Kind: StreamStart}}, report: &StreamReport{}}},
		beforeSend: cancel, // the turn is cancelled before any event lands
	}
	a := New(prov, tools.NewRegistry(), streamSilentLogger(), Options{})

	// Unbuffered and never read: the forwarding send blocks until the
	// cancelled context wins the select.
	events := make(chan StreamEvent)

	errCh := make(chan error, 1)
	go func() {
		_, err := a.TurnStream(ctx, sessionWith("go"), events)
		errCh <- err
	}()

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("TurnStream did not return after context cancellation")
	}
}

func TestTurnStream_CompressorOutcomesAreHandled(t *testing.T) {
	tests := []struct {
		name       string
		compressor Compressor
	}{
		{
			name: "rewrite",
			compressor: CompressorFunc(func(_ context.Context, s *Session) (bool, error) {
				s.Messages = []Message{NewUserText("compressed")}
				return true, nil
			}),
		},
		{
			name: "error is non-fatal",
			compressor: CompressorFunc(func(context.Context, *Session) (bool, error) {
				return false, errors.New("summariser down")
			}),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prov := &scriptedStreamer{steps: []streamStep{endTurnStep("ok")}}
			a := New(prov, tools.NewRegistry(), streamSilentLogger(),
				Options{Compressor: tc.compressor})

			events := make(chan StreamEvent, 8)
			collect := drain(events)

			final, err := a.TurnStream(context.Background(), sessionWith("hi"), events)
			require.NoError(t, err)
			assert.Equal(t, "ok", final.Content[0].Text)
			collect()
		})
	}
}

func TestTurnStream_ClosesEventChannelOnEveryExit(t *testing.T) {
	a := New(&scriptedStreamer{}, tools.NewRegistry(), streamSilentLogger(), Options{})
	events := make(chan StreamEvent, 1)

	_, err := a.TurnStream(context.Background(), NewSession("empty"), events)
	assert.ErrorIs(t, err, ErrEmptySession)

	_, open := <-events
	assert.False(t, open, "events channel must be closed even on early return")
}
