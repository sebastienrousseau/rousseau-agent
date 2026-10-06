package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// recordingProvider captures every Request the loop sends so tests
// can assert on the cache hint the provider adapter keys off.
type recordingProvider struct {
	stubProvider
	seen []Request
}

func (r *recordingProvider) Complete(ctx context.Context, req Request) (Response, error) {
	r.seen = append(r.seen, req)
	return r.stubProvider.Complete(ctx, req)
}

// recordingStreamer is the streaming twin.
type recordingStreamer struct {
	streamingStub
	seen []Request
}

func (r *recordingStreamer) Stream(ctx context.Context, req Request) (<-chan StreamEvent, <-chan StreamReport, error) {
	r.seen = append(r.seen, req)
	return r.streamingStub.Stream(ctx, req)
}

// Regression guard for the prompt-cache wiring: the Anthropic adapter
// emits cache_control only when Request.CacheableMessages > 0, and
// before this test nothing in the daemon path ever set it, so every
// turn was billed at uncached rates. Every iteration must mark the
// full message prefix it already holds.
func TestTurn_SetsCacheableMessagesToSessionLength(t *testing.T) {
	prov := &recordingProvider{stubProvider: stubProvider{responses: []Response{
		{
			Message: Message{Role: RoleAssistant, Content: []Content{
				{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "t1", Name: "echo", Input: []byte(`{}`)}},
			}},
			StopReason: StopToolUse,
		},
		{
			Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "done"}}},
			StopReason: StopEndTurn,
		},
	}}}
	reg := tools.NewRegistry()
	reg.MustRegister(&stubTool{name: "echo", out: "ok"})
	a := New(prov, reg, silentLogger(), Options{})
	s := NewSession("x")
	s.Append(NewUserText("hello"))

	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	require.Len(t, prov.seen, 2)
	// Iteration 1: one user message in the session.
	assert.Equal(t, 1, prov.seen[0].CacheableMessages)
	assert.Len(t, prov.seen[0].Messages, 1)
	// Iteration 2: user + assistant tool_use + user tool_result.
	assert.Equal(t, 3, prov.seen[1].CacheableMessages)
	assert.Len(t, prov.seen[1].Messages, 3)
}

func TestTurnStream_SetsCacheableMessagesToSessionLength(t *testing.T) {
	stub := &recordingStreamer{streamingStub: streamingStub{
		deltas:   []string{"ok"},
		final:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "ok"}}},
		stopWith: StopEndTurn,
	}}
	a := New(stub, tools.NewRegistry(), streamSilentLogger(), Options{})
	s := NewSession("x")
	s.Append(NewUserText("first"))
	s.Append(Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "reply"}}})
	s.Append(NewUserText("second"))

	events := make(chan StreamEvent, 8)
	go func() {
		for range events {
			// drain so the loop never blocks on a full channel
		}
	}()
	_, err := a.TurnStream(context.Background(), s, events)
	require.NoError(t, err)

	require.Len(t, stub.seen, 1)
	assert.Equal(t, 3, stub.seen[0].CacheableMessages)
}
