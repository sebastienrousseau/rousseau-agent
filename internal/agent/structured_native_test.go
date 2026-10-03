package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nativeStructured struct {
	stubCompProvider
	out    json.RawMessage
	called bool
}

func (n *nativeStructured) CompleteStructured(context.Context, Request, map[string]any) (json.RawMessage, error) {
	n.called = true
	return n.out, nil
}

var yesNoSchema = map[string]any{
	"type":     "object",
	"required": []any{"answer", "confidence"},
	"properties": map[string]any{
		"answer":     map[string]any{"type": "string", "enum": []any{"yes", "no"}},
		"confidence": map[string]any{"type": "number"},
	},
}

func TestStructured_PrefersNativeProvider(t *testing.T) {
	p := &nativeStructured{out: json.RawMessage(`{"answer":"yes","confidence":0.9}`)}
	resp, err := Structured(context.Background(), p, StructuredRequest{Prompt: "q", Schema: yesNoSchema})
	require.NoError(t, err)
	assert.True(t, p.called)
	assert.Zero(t, p.calls, "the prompt-only path is not used")
	assert.JSONEq(t, `{"answer":"yes","confidence":0.9}`, string(resp.Parsed))
}

// TestStructured_RejectsSchemaDrift pins validation on the prompt-only
// path: a reply that parses as JSON but breaks the schema is an error.
func TestStructured_RejectsSchemaDrift(t *testing.T) {
	for reply, want := range map[string]string{
		`{"answer":"maybe","confidence":0.5}`:  "not one of",
		`{"answer":"yes"}`:                     "missing required",
		`{"answer":"yes","confidence":"high"}`: "want number",
	} {
		p := &stubCompProvider{reply: reply}
		_, err := Structured(context.Background(), p, StructuredRequest{Prompt: "q", Schema: yesNoSchema})
		assert.ErrorContains(t, err, want, reply)
	}
	p := &stubCompProvider{reply: `{"answer":"no","confidence":0.2}`}
	_, err := Structured(context.Background(), p, StructuredRequest{Prompt: "q", Schema: yesNoSchema})
	assert.NoError(t, err)
}
