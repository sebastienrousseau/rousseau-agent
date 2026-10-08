package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// chattyTool returns a large output, like `cat` on a log file.
type chattyTool struct{ out string }

func (chattyTool) Name() string                { return "cat" }
func (chattyTool) Description() string         { return "prints a lot" }
func (chattyTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (c chattyTool) Execute(context.Context, json.RawMessage) (string, error) {
	return c.out, nil
}

// One oversized tool output must not fill the context window: the
// model sees the first MaxToolOutputBytes bytes plus a marker that
// says how much was cut.
func TestTurn_ToolOutputIsBounded(t *testing.T) {
	prov := &stubProvider{responses: []Response{
		{
			Message: Message{Role: RoleAssistant, Content: []Content{
				{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "c1", Name: "cat", Input: json.RawMessage(`{}`)}},
			}},
			StopReason: StopToolUse,
		},
		{
			Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "ok"}}},
			StopReason: StopEndTurn,
		},
	}}
	reg := tools.NewRegistry()
	reg.MustRegister(chattyTool{out: strings.Repeat("x", 1000)})
	a := New(prov, reg, silentLogger(), Options{MaxToolOutputBytes: 100})
	s := NewSession("x")
	s.Append(NewUserText("go"))

	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	res := s.Messages[2].Content[0].ToolResult
	require.NotNil(t, res)
	assert.False(t, res.IsError)
	assert.True(t, strings.HasPrefix(res.Output, strings.Repeat("x", 100)))
	assert.Contains(t, res.Output, "[output truncated: 100 of 1000 bytes shown]")
	assert.Less(t, len(res.Output), 200)
}

func TestBoundOutput(t *testing.T) {
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(), Options{})
	short := strings.Repeat("a", 10)
	assert.Equal(t, short, a.boundOutput(short), "under the default cap passes through")
	big := strings.Repeat("a", defaultMaxToolOutputBytes+1)
	assert.Contains(t, a.boundOutput(big), "[output truncated:", "default cap applies when unset")

	a = New(&stubProvider{}, tools.NewRegistry(), silentLogger(), Options{MaxToolOutputBytes: 5})
	// "héllo" is 6 bytes; a 5-byte cap lands after the two-byte é.
	got := a.boundOutput("héllo world")
	assert.True(t, strings.HasPrefix(got, "héll\n"), got)
	assert.Contains(t, got, "[output truncated: 5 of 12 bytes shown]")
	assert.True(t, strings.HasPrefix(a.boundOutput("abc€xyz"), "abc\n[output truncated: 3 of 9"), "cut lands before a multi-byte rune")
}

// failingTool returns a huge error, like an MCP server echoing a whole
// response body into its error text.
type failingTool struct{ msg string }

func (failingTool) Name() string                { return "fail" }
func (failingTool) Description() string         { return "fails loudly" }
func (failingTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (f failingTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", errors.New(f.msg)
}

// Error text is bounded like output: the model sees the cap plus a
// marker, not megabytes.
func TestTurn_ToolErrorTextIsBounded(t *testing.T) {
	prov := &stubProvider{responses: []Response{
		{
			Message: Message{Role: RoleAssistant, Content: []Content{
				{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "f1", Name: "fail", Input: json.RawMessage(`{}`)}},
			}},
			StopReason: StopToolUse,
		},
		{Message: Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "ok"}}}, StopReason: StopEndTurn},
	}}
	reg := tools.NewRegistry()
	reg.MustRegister(failingTool{msg: strings.Repeat("e", 10_000)})
	a := New(prov, reg, silentLogger(), Options{MaxToolOutputBytes: 100})
	s := NewSession("x")
	s.Append(NewUserText("go"))
	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)
	res := s.Messages[2].Content[0].ToolResult
	require.True(t, res.IsError)
	assert.Contains(t, res.Output, "[output truncated:")
	assert.Less(t, len(res.Output), 200)
}
