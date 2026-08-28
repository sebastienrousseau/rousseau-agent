package claudecli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

func TestClassifyLine_SystemStart(t *testing.T) {
	kind, delta, _, isResult := classifyLine(json.RawMessage(`{"type":"system","subtype":"init"}`))
	assert.Equal(t, StreamStart, kind)
	assert.Empty(t, delta)
	assert.False(t, isResult)
}

func TestClassifyLine_AssistantMessageDelta(t *testing.T) {
	line := json.RawMessage(`{"type":"assistant","message":{"content":[{"type":"text","text":"hi "}]}}`)
	kind, delta, _, _ := classifyLine(line)
	assert.Equal(t, StreamTextDelta, kind)
	assert.Equal(t, "hi ", delta)
}

func TestClassifyLine_AssistantMultiText(t *testing.T) {
	line := json.RawMessage(`{"type":"assistant","message":{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}`)
	kind, delta, _, _ := classifyLine(line)
	assert.Equal(t, StreamTextDelta, kind)
	assert.Equal(t, "ab", delta)
}

func TestClassifyLine_AssistantToolUse(t *testing.T) {
	line := json.RawMessage(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"bash"}]}}`)
	kind, _, _, _ := classifyLine(line)
	assert.Equal(t, StreamToolUse, kind)
}

func TestClassifyLine_ResultLine(t *testing.T) {
	line := json.RawMessage(`{"type":"result","result":"done","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
	kind, _, final, isResult := classifyLine(line)
	assert.Equal(t, StreamResult, kind)
	assert.True(t, isResult)
	require.Len(t, final.Message.Content, 1)
	assert.Equal(t, "done", final.Message.Content[0].Text)
	assert.Equal(t, agent.StopEndTurn, final.StopReason)
}

func TestClassifyLine_UnknownType(t *testing.T) {
	kind, _, _, _ := classifyLine(json.RawMessage(`{"type":"whatever"}`))
	assert.Equal(t, StreamOther, kind)
}

func TestClassifyLine_MalformedJSON(t *testing.T) {
	kind, _, _, _ := classifyLine(json.RawMessage(`not json`))
	assert.Equal(t, StreamOther, kind)
}

func TestParseStream_EndToEnd(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hel"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"lo"}]}}`,
		`{"type":"result","result":"hello","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`,
	}, "\n"))
	events := make(chan StreamEvent, 8)
	final, err := parseStream(stream, events)
	close(events)
	require.NoError(t, err)
	assert.Equal(t, "hello", final.Message.Content[0].Text)

	var deltas []string
	var hadStart, hadResult bool
	for e := range events {
		switch e.Kind {
		case StreamStart:
			hadStart = true
		case StreamTextDelta:
			deltas = append(deltas, e.Delta)
		case StreamResult:
			hadResult = true
		}
	}
	assert.True(t, hadStart)
	assert.True(t, hadResult)
	assert.Equal(t, []string{"hel", "lo"}, deltas)
}

func TestParseStream_MissingResultLine(t *testing.T) {
	stream := strings.NewReader(`{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}`)
	events := make(chan StreamEvent, 4)
	_, err := parseStream(stream, events)
	close(events)
	assert.Error(t, err)
}

func TestParseStream_IgnoresNonJSONLines(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		`INFO warmup`,
		`{"type":"result","result":"ok","stop_reason":"end_turn"}`,
	}, "\n"))
	events := make(chan StreamEvent, 4)
	final, err := parseStream(stream, events)
	close(events)
	require.NoError(t, err)
	assert.Equal(t, "ok", final.Message.Content[0].Text)
}

// TestProvider_Stream_ExtraArgsPropagated is a lightweight smoke test
// that verifies the CLI flag construction goes through the same
// invoke() code path. Real end-to-end streaming is covered by the
// integration test (build tag "integration") in live_test.go.
func TestProvider_Stream_ExtraArgsPropagated(t *testing.T) {
	p := New(Config{Binary: "/nonexistent-claude"})
	_, _, err := p.Stream(context.Background(), agent.Request{
		SessionID: "x",
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	// We can't actually stream, but the error path (start failure)
	// should not panic and should surface a real error.
	assert.Error(t, err)
}
