package claudecli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestCompleteStructured_UsesNativeSchemaWithNoTools pins the native
// structured path: --json-schema is passed, the CLI's
// structured_output is returned, and the call can use no tools at all
// (a decision call must never trigger the PreToolUse policy hook, or a
// risk-scoring approver would recurse into itself).
func TestCompleteStructured_UsesNativeSchemaWithNoTools(t *testing.T) {
	cli := newFakeCLI(t, `{"type":"result","subtype":"success","is_error":false,`+
		`"result":"{\"answer\":\"yes\"}","structured_output":{"answer":"yes"}}`, "", 0)
	p := New(Config{Binary: cli.path, Model: "haiku", Settings: `{"hooks":{}}`})

	schema := map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}}
	out, err := p.CompleteStructured(context.Background(), agent.Request{
		System:   "classify",
		Messages: []agent.Message{agent.NewUserText("is this risky?")},
	}, schema)
	require.NoError(t, err)
	assert.JSONEq(t, `{"answer":"yes"}`, string(out))

	argv := cli.argv(t)
	assert.Contains(t, argv, "--json-schema")
	assert.Contains(t, argv, "--strict-mcp-config")
	i := indexOf(argv, "--tools")
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, "", argv[i+1], "all built-in tools disabled")
	assert.NotContains(t, argv, "--settings", "no hooks on a tool-less call")
	assert.NotContains(t, argv, "--session-id")
	assert.Equal(t, "is this risky?", cli.stdin(t))
}

func TestCompleteStructured_MissingStructuredOutputErrors(t *testing.T) {
	cli := newFakeCLI(t, `{"type":"result","subtype":"success","is_error":false,"result":"not json"}`, "", 0)
	p := New(Config{Binary: cli.path})
	_, err := p.CompleteStructured(context.Background(), agent.Request{
		Messages: []agent.Message{agent.NewUserText("q")},
	}, map[string]any{"type": "object"})
	assert.ErrorContains(t, err, "structured_output")
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}
