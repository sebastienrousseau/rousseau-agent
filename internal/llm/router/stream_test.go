package router

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// fakeChild is a non-streaming provider that answers with its name.
type fakeChild struct{ name string }

func (f *fakeChild) Name() string { return f.name }

func (f *fakeChild) Complete(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{
		Message:    agent.Message{Role: agent.RoleAssistant, Content: []agent.Content{{Kind: agent.ContentText, Text: f.name}}},
		StopReason: agent.StopEndTurn,
		Model:      f.name,
	}, nil
}

// streamStub streams two deltas and records that Stream, not Complete,
// was used.
type streamStub struct {
	fakeChild
	streamed bool
}

func (s *streamStub) Stream(_ context.Context, _ agent.Request) (<-chan agent.StreamEvent, <-chan agent.StreamReport, error) {
	s.streamed = true
	events := make(chan agent.StreamEvent, 2)
	report := make(chan agent.StreamReport, 1)
	events <- agent.StreamEvent{Kind: agent.StreamTextDelta, Delta: "par"}
	events <- agent.StreamEvent{Kind: agent.StreamTextDelta, Delta: "tial"}
	close(events)
	resp, _ := s.Complete(context.Background(), agent.Request{}) //nolint:errcheck // stub never fails
	report <- agent.StreamReport{Response: resp}
	close(report)
	return events, report, nil
}

type failing struct{ fakeChild }

func (*failing) Complete(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{}, errors.New("upstream down")
}

func drain(t *testing.T, events <-chan agent.StreamEvent, report <-chan agent.StreamReport) ([]agent.StreamEvent, agent.StreamReport) {
	t.Helper()
	var got []agent.StreamEvent
	for ev := range events {
		got = append(got, ev)
	}
	rep, ok := <-report
	require.True(t, ok, "exactly one report is delivered")
	_, more := <-report
	assert.False(t, more, "report channel is closed after the report")
	return got, rep
}

func userReq(text string) agent.Request {
	return agent.Request{Messages: []agent.Message{agent.NewUserText(text)}}
}

// The router is a streaming provider, so wrapping streaming children
// in it no longer turns streaming off.
func TestRouter_IsStreamingProvider(t *testing.T) {
	var p agent.Provider = &Router{}
	_, ok := p.(agent.StreamingProvider)
	assert.True(t, ok)
}

func TestRouter_StreamDelegatesToStreamingChild(t *testing.T) {
	fast := &streamStub{fakeChild: fakeChild{name: "fast"}}
	r, err := New(Options{
		Default:   "big",
		Rules:     []Rule{{Name: "short", MessageLenMax: 10, Use: "fast"}},
		Providers: map[string]agent.Provider{"big": &fakeChild{name: "big"}, "fast": fast},
	})
	require.NoError(t, err)

	events, report, err := r.Stream(context.Background(), userReq("hi"))
	require.NoError(t, err)
	got, rep := drain(t, events, report)
	assert.True(t, fast.streamed, "the rule's child streamed")
	require.Len(t, got, 2)
	assert.Equal(t, "par", got[0].Delta)
	require.NoError(t, rep.Err)
	assert.Equal(t, "fast", rep.Response.Model)
}

// A child that cannot stream is answered by Complete and replayed as
// start, one text delta, and result.
func TestRouter_StreamEmulatesForNonStreamingChild(t *testing.T) {
	r, err := New(Options{Default: "big", Providers: map[string]agent.Provider{"big": &fakeChild{name: "big"}}})
	require.NoError(t, err)

	events, report, err := r.Stream(context.Background(), userReq("a longer question"))
	require.NoError(t, err)
	got, rep := drain(t, events, report)
	require.Len(t, got, 3)
	assert.Equal(t, agent.StreamStart, got[0].Kind)
	assert.Equal(t, agent.StreamTextDelta, got[1].Kind)
	assert.Equal(t, "big", got[1].Delta)
	assert.Equal(t, agent.StreamResult, got[2].Kind)
	require.NoError(t, rep.Err)
	assert.Equal(t, "big", rep.Response.Model)
}

func TestRouter_StreamEmulationReportsChildError(t *testing.T) {
	r, err := New(Options{Default: "down", Providers: map[string]agent.Provider{"down": &failing{fakeChild{name: "down"}}}})
	require.NoError(t, err)

	events, report, err := r.Stream(context.Background(), userReq("hi"))
	require.NoError(t, err)
	got, rep := drain(t, events, report)
	require.Len(t, got, 1, "only start is emitted before a failure")
	assert.EqualError(t, rep.Err, "upstream down")
}

// End to end: an agent over a router streams the routed child's
// deltas to the caller.
func TestRouter_AgentTurnStreamThroughRouter(t *testing.T) {
	fast := &streamStub{fakeChild: fakeChild{name: "fast"}}
	r, err := New(Options{Default: "fast", Providers: map[string]agent.Provider{"fast": fast}})
	require.NoError(t, err)
	a := agent.New(r, tools.NewRegistry(), slog.New(slog.NewTextHandler(io.Discard, nil)), agent.Options{})
	s := agent.NewSession("x")
	s.Append(agent.NewUserText("hi"))

	out := make(chan agent.StreamEvent, 16)
	final, err := a.TurnStream(context.Background(), s, out)
	require.NoError(t, err)
	var deltas string
	for ev := range out {
		if ev.Kind == agent.StreamTextDelta {
			deltas += ev.Delta
		}
	}
	assert.True(t, fast.streamed)
	assert.Equal(t, "partial", deltas)
	assert.Equal(t, "fast", final.Content[0].Text)
}
