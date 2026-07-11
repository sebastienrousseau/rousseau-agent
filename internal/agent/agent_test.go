package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

type stubProvider struct {
	responses []Response
	err       error
	i         int
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) Complete(_ context.Context, _ Request) (Response, error) {
	if s.err != nil {
		return Response{}, s.err
	}
	if s.i >= len(s.responses) {
		return Response{}, errors.New("no more responses")
	}
	r := s.responses[s.i]
	s.i++
	return r, nil
}

type stubTool struct {
	name string
	out  string
	err  error
}

func (s *stubTool) Name() string                   { return s.name }
func (s *stubTool) Description() string            { return "stub" }
func (s *stubTool) InputSchema() map[string]any    { return map[string]any{"type": "object"} }
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return s.out, s.err
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTurn_EmptySession(t *testing.T) {
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(), Options{})
	_, err := a.Turn(context.Background(), NewSession("x"))
	assert.ErrorIs(t, err, ErrEmptySession)
}

func TestTurn_EndTurnSimple(t *testing.T) {
	prov := &stubProvider{
		responses: []Response{
			{
				Message: Message{
					Role:    RoleAssistant,
					Content: []Content{{Kind: ContentText, Text: "hi"}},
				},
				StopReason: StopEndTurn,
			},
		},
	}
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{})
	s := NewSession("x")
	s.Append(NewUserText("hello"))

	final, err := a.Turn(context.Background(), s)
	require.NoError(t, err)
	require.Len(t, final.Content, 1)
	assert.Equal(t, "hi", final.Content[0].Text)
	assert.Len(t, s.Messages, 2)
}

func TestTurn_ToolUseThenEnd(t *testing.T) {
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&stubTool{name: "echo", out: "pong"}))

	prov := &stubProvider{
		responses: []Response{
			{
				Message: Message{
					Role: RoleAssistant,
					Content: []Content{{
						Kind: ContentToolUse,
						ToolUse: &ToolUse{
							ID: "call_1", Name: "echo", Input: json.RawMessage(`{}`),
						},
					}},
				},
				StopReason: StopToolUse,
			},
			{
				Message: Message{
					Role:    RoleAssistant,
					Content: []Content{{Kind: ContentText, Text: "done"}},
				},
				StopReason: StopEndTurn,
			},
		},
	}
	a := New(prov, registry, silentLogger(), Options{})
	s := NewSession("x")
	s.Append(NewUserText("call echo"))

	final, err := a.Turn(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, "done", final.Content[0].Text)
	require.Len(t, s.Messages, 4)
	assert.Equal(t, RoleUser, s.Messages[2].Role)
	require.Len(t, s.Messages[2].Content, 1)
	assert.Equal(t, ContentToolResult, s.Messages[2].Content[0].Kind)
	assert.Equal(t, "pong", s.Messages[2].Content[0].ToolResult.Output)
}

func TestTurn_ToolNotFound(t *testing.T) {
	prov := &stubProvider{
		responses: []Response{
			{
				Message: Message{
					Role: RoleAssistant,
					Content: []Content{{
						Kind:    ContentToolUse,
						ToolUse: &ToolUse{ID: "x", Name: "missing", Input: json.RawMessage(`{}`)},
					}},
				},
				StopReason: StopToolUse,
			},
		},
	}
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{})
	s := NewSession("x")
	s.Append(NewUserText("hello"))
	_, err := a.Turn(context.Background(), s)
	assert.ErrorIs(t, err, ErrToolNotFound)
}

func TestTurn_MaxIterations(t *testing.T) {
	loop := Response{
		Message: Message{
			Role: RoleAssistant,
			Content: []Content{{
				Kind:    ContentToolUse,
				ToolUse: &ToolUse{ID: "x", Name: "echo", Input: json.RawMessage(`{}`)},
			}},
		},
		StopReason: StopToolUse,
	}
	prov := &stubProvider{responses: []Response{loop, loop, loop, loop}}
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&stubTool{name: "echo", out: ""}))

	a := New(prov, registry, silentLogger(), Options{MaxIterations: 3})
	s := NewSession("x")
	s.Append(NewUserText("go"))

	_, err := a.Turn(context.Background(), s)
	assert.ErrorIs(t, err, ErrMaxIterations)
}
