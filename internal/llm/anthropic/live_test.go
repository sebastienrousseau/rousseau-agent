package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

const completeFixture = `{
  "id": "msg_01",
  "type": "message",
  "role": "assistant",
  "content": [{"type": "text", "text": "hi from anthropic"}],
  "model": "claude-sonnet-4-6",
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {"input_tokens": 5, "output_tokens": 3}
}`

func TestComplete_MockedHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completeFixture)) //nolint:errcheck // test fixture
	}))
	defer server.Close()

	p := &Provider{
		client: sdk.NewClient(
			option.WithAPIKey("sk-test"),
			option.WithBaseURL(server.URL),
		),
		cfg: Config{APIKey: "sk-test", Model: "claude-sonnet-4-6", MaxTokens: 4096},
	}

	resp, err := p.Complete(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hello")},
	})
	require.NoError(t, err)
	require.Len(t, resp.Message.Content, 1)
	assert.Equal(t, "hi from anthropic", resp.Message.Content[0].Text)
	assert.Equal(t, agent.StopEndTurn, resp.StopReason)
	assert.Equal(t, 5, resp.Usage.InputTokens)
	assert.Equal(t, 3, resp.Usage.OutputTokens)
}

// SSE fixture representing a real anthropic streaming exchange.
const streamFixture = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`

func TestStream_MockedHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(streamFixture)) //nolint:errcheck // test fixture
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	p := &Provider{
		client: sdk.NewClient(
			option.WithAPIKey("sk-test"),
			option.WithBaseURL(server.URL),
		),
		cfg: Config{APIKey: "sk-test", Model: "claude-sonnet-4-6", MaxTokens: 4096},
	}

	events, reports, err := p.Stream(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hello")},
	})
	require.NoError(t, err)

	var deltas []string
	var haveStart bool
	var haveResult bool
	for e := range events {
		switch e.Kind {
		case agent.StreamStart:
			haveStart = true
		case agent.StreamTextDelta:
			deltas = append(deltas, e.Delta)
		case agent.StreamResult:
			haveResult = true
		}
	}
	report := <-reports
	require.NoError(t, report.Err)
	assert.True(t, haveStart)
	assert.True(t, haveResult)
	assert.Equal(t, []string{"hel", "lo"}, deltas)
	require.Len(t, report.Response.Message.Content, 1)
	assert.Equal(t, "hello", report.Response.Message.Content[0].Text)
	assert.Equal(t, agent.StopEndTurn, report.Response.StopReason)
}
