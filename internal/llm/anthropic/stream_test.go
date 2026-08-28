package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// sseEvent is one server-sent event in the Messages streaming wire
// format: a named event plus a single JSON data line.
type sseEvent struct {
	name string
	data string
}

// writeSSE serialises events in the exact framing the SDK's decoder
// expects (blank line terminates each event).
func writeSSE(w io.Writer, events []sseEvent) {
	for _, e := range events {
		_, _ = io.WriteString(w, "event: "+e.name+"\ndata: "+e.data+"\n\n") //nolint:errcheck // test fixture
	}
}

// streamServer serves the supplied SSE script and records the decoded
// request body so tests can assert on what was actually sent.
func streamServer(t *testing.T, events []sseEvent, captured *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captured != nil {
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, captured))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(w, events)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func streamProvider(srv *httptest.Server) *Provider {
	return &Provider{
		client: sdk.NewClient(option.WithAPIKey("sk-test"), option.WithBaseURL(srv.URL)),
		cfg:    Config{APIKey: "sk-test", Model: "claude-sonnet-4-6", MaxTokens: 256},
	}
}

// drain collects every event and the terminal report.
func drain(t *testing.T, evs <-chan agent.StreamEvent, rep <-chan agent.StreamReport) ([]agent.StreamEvent, agent.StreamReport) {
	t.Helper()
	var got []agent.StreamEvent
	for e := range evs {
		got = append(got, e)
	}
	report, ok := <-rep
	require.True(t, ok, "report channel closed without a value")
	return got, report
}

func kinds(evs []agent.StreamEvent) []agent.StreamEventKind {
	out := make([]agent.StreamEventKind, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

// happyScript is a full text-then-tool_use turn.
var happyScript = []sseEvent{
	{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":null,"usage":{"input_tokens":11,"output_tokens":1}}}`},
	{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Look"}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ing"}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":0}`},
	{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"grep","input":{}}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"pattern\":\"x\"}"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":1}`},
	{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":9}}`},
	{"message_stop", `{"type":"message_stop"}`},
}

// TestStream_AssemblesTextAndToolUse walks the whole streaming path:
// event fan-out, delta aggregation, tool_use detection, and the final
// assembled Response.
func TestStream_AssemblesTextAndToolUse(t *testing.T) {
	var body map[string]any
	srv := streamServer(t, happyScript, &body)
	p := streamProvider(srv)

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		System:            "be terse",
		CacheableMessages: 1,
		Messages:          []agent.Message{agent.NewUserText("find x")},
		Tools: []tools.Definition{{
			Name: "grep", Description: "search", InputSchema: map[string]any{"type": "object"},
		}},
	})
	require.NoError(t, err)

	got, report := drain(t, evs, rep)
	require.NoError(t, report.Err)

	// Exactly one start, one delta per non-empty text_delta, one
	// tool-use notice, one terminal result.
	assert.Equal(t, []agent.StreamEventKind{
		agent.StreamStart,
		agent.StreamTextDelta,
		agent.StreamTextDelta,
		agent.StreamToolUse,
		agent.StreamResult,
	}, kinds(got))

	var text strings.Builder
	for _, e := range got {
		text.WriteString(e.Delta)
	}
	assert.Equal(t, "Looking", text.String())

	require.Len(t, report.Response.Message.Content, 2)
	assert.Equal(t, agent.ContentText, report.Response.Message.Content[0].Kind)
	assert.Equal(t, "Looking", report.Response.Message.Content[0].Text)

	tu := report.Response.Message.Content[1].ToolUse
	require.NotNil(t, tu)
	assert.Equal(t, "toolu_1", tu.ID)
	assert.Equal(t, "grep", tu.Name)
	assert.JSONEq(t, `{"pattern":"x"}`, string(tu.Input))

	assert.Equal(t, agent.StopToolUse, report.Response.StopReason)
	assert.Equal(t, 11, report.Response.Usage.InputTokens)
	assert.Equal(t, 9, report.Response.Usage.OutputTokens)

	// The streaming request carries the same system / tools / cache
	// decorations as Complete.
	require.NotNil(t, body)
	assert.Equal(t, true, body["stream"])
	sys, ok := body["system"].([]any)
	require.True(t, ok, "system: %v", body["system"])
	require.Len(t, sys, 1)
	assert.Equal(t, map[string]any{"type": "ephemeral", "ttl": "1h"},
		sys[0].(map[string]any)["cache_control"])
	toolList, ok := body["tools"].([]any)
	require.True(t, ok)
	require.Len(t, toolList, 1)
	assert.NotNil(t, toolList[0].(map[string]any)["cache_control"],
		"final tool must carry the 1h cache breakpoint")
	msgs := body["messages"].([]any)
	require.Len(t, msgs, 1)
	content := msgs[0].(map[string]any)["content"].([]any)
	assert.NotNil(t, content[0].(map[string]any)["cache_control"],
		"CacheableMessages must mark the trailing message")
}

// TestStream_NoSystemOrToolsOmitsFields is the negative of the above:
// with neither System nor Tools nothing extra is serialised.
func TestStream_NoSystemOrToolsOmitsFields(t *testing.T) {
	var body map[string]any
	srv := streamServer(t, []sseEvent{
		{"message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}, &body)

	evs, rep, err := streamProvider(srv).Stream(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	got, report := drain(t, evs, rep)
	require.NoError(t, report.Err)
	assert.Equal(t, "ok", report.Response.Message.Content[0].Text)
	assert.Equal(t, agent.StopEndTurn, report.Response.StopReason)
	assert.Equal(t, []agent.StreamEventKind{
		agent.StreamStart, agent.StreamTextDelta, agent.StreamResult,
	}, kinds(got))
	assert.NotContains(t, body, "system")
	assert.NotContains(t, body, "tools")
}

// TestStream_ConversionErrorIsSynchronous proves a bad message list
// fails before any channel is created or any request is issued.
func TestStream_ConversionErrorIsSynchronous(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("transport must not be reached")
	}))
	defer srv.Close()

	evs, rep, err := streamProvider(srv).Stream(context.Background(), agent.Request{
		Messages: []agent.Message{{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentToolUse}}, // missing payload
		}},
	})
	require.Error(t, err)
	assert.Nil(t, evs)
	assert.Nil(t, rep)
}

// TestStream_AccumulateErrorSurfaces feeds an out-of-order
// content_block_start (index 1 with no index 0), which the SDK
// accumulator rejects.
func TestStream_AccumulateErrorSurfaces(t *testing.T) {
	srv := streamServer(t, []sseEvent{
		{"message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`},
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}, nil)

	evs, rep, err := streamProvider(srv).Stream(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	got, report := drain(t, evs, rep)
	require.Error(t, report.Err)
	assert.Contains(t, report.Err.Error(), "anthropic: accumulate")
	assert.Empty(t, report.Response.Message.Content)
	// The start event still reached the caller before the failure.
	assert.Equal(t, []agent.StreamEventKind{agent.StreamStart}, kinds(got))
}

// TestStream_UpstreamErrorEventSurfaces covers stream.Err(): the API
// signals a mid-stream failure with an `error` event.
func TestStream_UpstreamErrorEventSurfaces(t *testing.T) {
	srv := streamServer(t, []sseEvent{
		{"message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`},
		{"error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
	}, nil)

	evs, rep, err := streamProvider(srv).Stream(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := drain(t, evs, rep)
	require.Error(t, report.Err)
	assert.Contains(t, report.Err.Error(), "anthropic: stream")
	assert.Contains(t, report.Err.Error(), "Overloaded")
}

// TestStream_TruncatedStreamDeliversDeltasOnly documents what callers
// get when the connection ends after the deltas but before
// content_block_stop/message_stop: the incremental events are all
// delivered, but the accumulator never refreshes the block's wire JSON,
// so the assembled final message is empty. Consumers that need the full
// text must concatenate the deltas rather than trust Response on a
// truncated stream.
func TestStream_TruncatedStreamDeliversDeltasOnly(t *testing.T) {
	srv := streamServer(t, []sseEvent{
		{"message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half"}}`},
	}, nil)

	evs, rep, err := streamProvider(srv).Stream(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	got, report := drain(t, evs, rep)
	require.NoError(t, report.Err)

	assert.Equal(t, []agent.StreamEventKind{
		agent.StreamStart, agent.StreamTextDelta, agent.StreamResult,
	}, kinds(got))
	var streamed strings.Builder
	for _, e := range got {
		streamed.WriteString(e.Delta)
	}
	assert.Equal(t, "half", streamed.String(), "every delta must still reach the caller")

	require.Len(t, report.Response.Message.Content, 1)
	assert.Empty(t, report.Response.Message.Content[0].Text,
		"an unterminated block is not folded into the assembled message")
	assert.Equal(t, agent.StopOther, report.Response.StopReason)
}

// TestStream_UnsupportedBlockFailsAssembly: a thinking block has no
// agent.Content equivalent, so assembling the final message errors.
func TestStream_UnsupportedBlockFailsAssembly(t *testing.T) {
	srv := streamServer(t, []sseEvent{
		{"message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_stop", `{"type":"message_stop"}`},
	}, nil)

	evs, rep, err := streamProvider(srv).Stream(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	got, report := drain(t, evs, rep)
	require.Error(t, report.Err)
	assert.Contains(t, report.Err.Error(), "unsupported content block")
	// No StreamResult is emitted when assembly fails.
	assert.NotContains(t, kinds(got), agent.StreamResult)
}

// -- transport double for the Close() error branch ---------------------

// closeErrTransport replays a canned SSE body whose Close reports an
// error, which is how a broken keep-alive connection surfaces.
type closeErrTransport struct {
	payload  string
	closeErr error
}

func (c *closeErrTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &errCloser{Reader: strings.NewReader(c.payload), err: c.closeErr},
		Request:    req,
	}, nil
}

type errCloser struct {
	io.Reader
	err error
}

func (e *errCloser) Close() error { return e.err }

func TestStream_CloseErrorSurfacesWhenStreamSucceeded(t *testing.T) {
	var sb strings.Builder
	writeSSE(&sb, []sseEvent{
		{"message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`},
		{"message_stop", `{"type":"message_stop"}`},
	})

	closeErr := errors.New("body close failed")
	p := &Provider{
		client: sdk.NewClient(
			option.WithAPIKey("sk-test"),
			option.WithBaseURL("https://api.invalid"),
			option.WithHTTPClient(&http.Client{Transport: &closeErrTransport{
				payload: sb.String(), closeErr: closeErr,
			}}),
		),
		cfg: Config{APIKey: "sk-test", Model: "m", MaxTokens: 64},
	}

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := drain(t, evs, rep)
	require.Error(t, report.Err)
	assert.Contains(t, report.Err.Error(), "anthropic: close stream")
	assert.ErrorIs(t, report.Err, closeErr)
	// The response is still delivered alongside the close failure.
	require.Len(t, report.Response.Message.Content, 1)
	assert.Equal(t, "done", report.Response.Message.Content[0].Text)
}

func TestFromAssembledMessage_NilRejected(t *testing.T) {
	_, err := fromAssembledMessage(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil assembled message")
}

func TestExtractDeltaText_NonTextDeltaIsEmpty(t *testing.T) {
	var evt sdk.ContentBlockDeltaEvent
	require.NoError(t, json.Unmarshal([]byte(
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
	), &evt))
	assert.Empty(t, extractDeltaText(evt))
}

func TestIsToolUseStart_Table(t *testing.T) {
	cases := map[string]bool{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"n","input":{}}}`: true,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`:                          false,
	}
	for raw, want := range cases {
		var evt sdk.ContentBlockStartEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &evt))
		assert.Equal(t, want, isToolUseStart(evt), raw)
	}
}
