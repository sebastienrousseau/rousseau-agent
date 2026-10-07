package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

type recordingCost struct{ events []CostEvent }

func (r *recordingCost) Record(_ context.Context, e CostEvent) error {
	r.events = append(r.events, e)
	return nil
}

// Before the loops were unified the streaming path skipped cost
// recording entirely, so the daemon (which prefers TurnStream) never
// produced a cost ledger. Both entry points must record the same
// event.
func TestTurnStream_RecordsCostLikeTurn(t *testing.T) {
	final := Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "ok"}}}
	for _, tc := range []struct {
		name   string
		drive  func(a *Agent, s *Session) error
		usage  Usage
		stream bool
	}{
		{"Turn", func(a *Agent, s *Session) error { _, err := a.Turn(context.Background(), s); return err }, Usage{InputTokens: 7, OutputTokens: 3}, false},
		{"TurnStream", func(a *Agent, s *Session) error {
			ev := make(chan StreamEvent, 8)
			go func() {
				for range ev {
					// drain
				}
			}()
			_, err := a.TurnStream(context.Background(), s, ev)
			return err
		}, Usage{InputTokens: 11, OutputTokens: 5}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingCost{}
			var prov Provider
			if tc.stream {
				prov = &usageStreamer{streamingStub: streamingStub{deltas: []string{"ok"}, final: final, stopWith: StopEndTurn}, usage: tc.usage}
			} else {
				prov = &stubProvider{responses: []Response{{Message: final, StopReason: StopEndTurn, Usage: tc.usage, Model: "m"}}}
			}
			a := New(prov, tools.NewRegistry(), silentLogger(), Options{CostRecorder: rec})
			s := NewSession("x")
			s.Append(NewUserText("hi"))
			require.NoError(t, tc.drive(a, s))

			require.Len(t, rec.events, 1)
			assert.Equal(t, s.ID, rec.events[0].SessionID)
			assert.Equal(t, tc.usage, rec.events[0].Usage)
		})
	}
}

// usageStreamer is streamingStub with a Usage on its report so cost
// recording has something to record.
type usageStreamer struct {
	streamingStub
	usage Usage
}

func (u *usageStreamer) Stream(ctx context.Context, req Request) (<-chan StreamEvent, <-chan StreamReport, error) {
	events, report, err := u.streamingStub.Stream(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	out := make(chan StreamReport, 1)
	go func() {
		defer close(out)
		for r := range report {
			r.Response.Usage = u.usage
			r.Response.Model = "m"
			out <- r
		}
	}()
	return events, out, nil
}

// A provider that cannot stream still goes through the unified loop
// and records cost.
func TestTurnStream_NonStreamingProviderStillRecordsCost(t *testing.T) {
	rec := &recordingCost{}
	prov := &stubProvider{responses: []Response{{
		Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "ok"}}},
		StopReason: StopEndTurn,
		Usage:      Usage{InputTokens: 2, OutputTokens: 1},
	}}}
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{CostRecorder: rec})
	s := NewSession("x")
	s.Append(NewUserText("hi"))
	ev := make(chan StreamEvent, 8)
	_, err := a.TurnStream(context.Background(), s, ev)
	require.NoError(t, err)
	_, open := <-ev
	assert.False(t, open, "events channel is closed on return")
	require.Len(t, rec.events, 1)
	assert.Equal(t, 2, rec.events[0].Usage.InputTokens)
}
