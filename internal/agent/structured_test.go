package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type structuredProvider struct {
	reply string
	err   error
	seen  Request
}

func (s *structuredProvider) Name() string { return "stub" }
func (s *structuredProvider) Complete(_ context.Context, r Request) (Response, error) {
	s.seen = r
	if s.err != nil {
		return Response{}, s.err
	}
	return Response{
		Message: Message{
			Role:    RoleAssistant,
			Content: []Content{{Kind: ContentText, Text: s.reply}},
		},
		StopReason: StopEndTurn,
	}, nil
}

func TestStructured_NilProviderErrors(t *testing.T) {
	_, err := Structured(context.Background(), nil, StructuredRequest{
		Schema: map[string]any{"type": "object"},
	})
	assert.Error(t, err)
}

func TestStructured_EmptySchemaErrors(t *testing.T) {
	_, err := Structured(context.Background(), &structuredProvider{}, StructuredRequest{})
	assert.Error(t, err)
}

func TestStructured_HappyPath(t *testing.T) {
	p := &structuredProvider{reply: `{"answer": "yes", "confidence": 0.9}`}
	got, err := Structured(context.Background(), p, StructuredRequest{
		SystemPrompt: "you help",
		Prompt:       "answer briefly",
		Schema: map[string]any{
			"type":     "object",
			"required": []string{"answer"},
			"properties": map[string]any{
				"answer":     map[string]any{"type": "string"},
				"confidence": map[string]any{"type": "number"},
			},
		},
	})
	require.NoError(t, err)
	assert.Contains(t, got.Raw, "yes")

	var parsed struct {
		Answer     string  `json:"answer"`
		Confidence float64 `json:"confidence"`
	}
	require.NoError(t, json.Unmarshal(got.Parsed, &parsed))
	assert.Equal(t, "yes", parsed.Answer)
	assert.InEpsilon(t, 0.9, parsed.Confidence, 0.001)

	// System prompt must reference the schema.
	assert.Contains(t, p.seen.System, "JSON Schema")
	assert.Contains(t, p.seen.System, "you help")
}

func TestStructured_StripsMarkdownFence(t *testing.T) {
	p := &structuredProvider{reply: "Here is the JSON:\n```json\n{\"ok\": true}\n```"}
	got, err := Structured(context.Background(), p, StructuredRequest{
		Prompt: "flag",
		Schema: map[string]any{"type": "object"},
	})
	require.NoError(t, err)
	assert.Equal(t, `{"ok": true}`, string(got.Parsed))
}

func TestStructured_ArrayResponse(t *testing.T) {
	p := &structuredProvider{reply: `["a", "b", "c"]`}
	got, err := Structured(context.Background(), p, StructuredRequest{
		Prompt: "list",
		Schema: map[string]any{"type": "array"},
	})
	require.NoError(t, err)
	var list []string
	require.NoError(t, json.Unmarshal(got.Parsed, &list))
	assert.Equal(t, []string{"a", "b", "c"}, list)
}

func TestStructured_ProviderError(t *testing.T) {
	p := &structuredProvider{err: errors.New("api down")}
	_, err := Structured(context.Background(), p, StructuredRequest{
		Prompt: "x",
		Schema: map[string]any{"type": "object"},
	})
	assert.Error(t, err)
}

func TestStructured_UnparseableRepliesErrorGracefully(t *testing.T) {
	p := &structuredProvider{reply: "this is not JSON at all"}
	_, err := Structured(context.Background(), p, StructuredRequest{
		Prompt: "x",
		Schema: map[string]any{"type": "object"},
	})
	assert.Error(t, err)
}

func TestStructured_EmptyResponseErrors(t *testing.T) {
	p := &structuredProvider{reply: ""}
	_, err := Structured(context.Background(), p, StructuredRequest{
		Prompt: "x",
		Schema: map[string]any{"type": "object"},
	})
	assert.Error(t, err)
}

func TestExtractJSON_LeadingPreamble(t *testing.T) {
	obj, err := extractJSON(`Sure, here you go: {"a": 1}. Hope this helps!`)
	require.NoError(t, err)
	assert.Equal(t, `{"a": 1}`, string(obj))
}

func TestExtractJSON_NoJSONReturnsError(t *testing.T) {
	_, err := extractJSON(`I do not know how to answer this in JSON.`)
	assert.Error(t, err)
}

func TestExtractJSON_InvalidJSONReturnsError(t *testing.T) {
	_, err := extractJSON(`{invalid}`)
	assert.Error(t, err)
}
