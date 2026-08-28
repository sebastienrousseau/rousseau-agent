package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubCompProvider struct {
	reply string
	err   error
	calls int
}

func (p *stubCompProvider) Name() string { return "stub" }
func (p *stubCompProvider) Complete(_ context.Context, _ Request) (Response, error) {
	p.calls++
	if p.err != nil {
		return Response{}, p.err
	}
	return Response{
		Message: Message{
			Role:    RoleAssistant,
			Content: []Content{{Kind: ContentText, Text: p.reply}},
		},
		StopReason: StopEndTurn,
	}, nil
}

func TestNoopCompressor(t *testing.T) {
	changed, err := NoopCompressor{}.Compress(context.Background(), NewSession("x"))
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestCompressorFunc_Adapts(t *testing.T) {
	called := false
	var fn CompressorFunc = func(context.Context, *Session) (bool, error) {
		called = true
		return true, nil
	}
	changed, err := fn.Compress(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, called)
	assert.True(t, changed)
}

func TestLLMCompressor_BelowThresholdSkips(t *testing.T) {
	prov := &stubCompProvider{reply: "summary"}
	c := &LLMCompressor{Provider: prov, TriggerMessages: 20, KeepRecent: 3}
	s := NewSession("x")
	for i := 0; i < 5; i++ {
		s.Append(NewUserText("msg"))
	}
	changed, err := c.Compress(context.Background(), s)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, 0, prov.calls)
}

func TestLLMCompressor_CondensesLongSession(t *testing.T) {
	prov := &stubCompProvider{reply: "The user asked for X. The assistant did Y."}
	c := &LLMCompressor{Provider: prov, TriggerMessages: 10, KeepRecent: 4}
	s := NewSession("x")
	for i := 0; i < 20; i++ {
		s.Append(NewUserText("some content here"))
	}
	before := len(s.Messages)
	changed, err := c.Compress(context.Background(), s)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 1, prov.calls)
	// Synthetic summary + 4 recent messages.
	assert.Equal(t, 1+4, len(s.Messages))
	assert.Less(t, len(s.Messages), before)
	assert.True(t, strings.HasPrefix(s.Messages[0].Content[0].Text, DefaultCompressorMarker))
	assert.Contains(t, s.Messages[0].Content[0].Text, "user asked for X")
}

func TestLLMCompressor_HeadAlreadyCompressedIsSkipped(t *testing.T) {
	prov := &stubCompProvider{reply: "again"}
	c := &LLMCompressor{Provider: prov, TriggerMessages: 5, KeepRecent: 2}
	s := NewSession("x")
	// Prime with an already-compressed head.
	s.Append(Message{
		Role:    RoleUser,
		Content: []Content{{Kind: ContentText, Text: DefaultCompressorMarker + " (summary)"}},
	})
	for i := 0; i < 4; i++ {
		s.Append(NewUserText("hi"))
	}
	changed, err := c.Compress(context.Background(), s)
	require.NoError(t, err)
	assert.False(t, changed, "already-compressed head should short-circuit")
	assert.Equal(t, 0, prov.calls)
}

func TestLLMCompressor_ProviderErrorSurfaces(t *testing.T) {
	prov := &stubCompProvider{err: errors.New("api down")}
	c := &LLMCompressor{Provider: prov, TriggerMessages: 3, KeepRecent: 1}
	s := NewSession("x")
	for i := 0; i < 5; i++ {
		s.Append(NewUserText("m"))
	}
	changed, err := c.Compress(context.Background(), s)
	require.Error(t, err)
	assert.False(t, changed)
}

func TestLLMCompressor_NilSessionOrProviderIsNoop(t *testing.T) {
	c := &LLMCompressor{TriggerMessages: 3}
	changed, err := c.Compress(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, changed)

	c.Provider = &stubCompProvider{reply: "sum"}
	c.Provider = nil
	s := NewSession("x")
	changed, err = c.Compress(context.Background(), s)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestLLMCompressor_SummarisationIncludesToolBlocks(t *testing.T) {
	prov := &stubCompProvider{reply: "compact"}
	c := &LLMCompressor{Provider: prov, TriggerMessages: 3, KeepRecent: 1}
	s := NewSession("x")
	s.Append(Message{
		Role: RoleAssistant,
		Content: []Content{
			{Kind: ContentText, Text: "let me look"},
			{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "1", Name: "grep", Input: []byte(`{"pattern":"x"}`)}},
		},
	})
	s.Append(Message{
		Role: RoleUser,
		Content: []Content{
			{Kind: ContentToolResult, ToolResult: &ToolResult{ToolUseID: "1", Output: "some output"}},
		},
	})
	s.Append(NewUserText("thanks"))
	changed, err := c.Compress(context.Background(), s)
	require.NoError(t, err)
	assert.True(t, changed)
}

func TestItoa(t *testing.T) {
	assert.Equal(t, "0", itoa(0))
	assert.Equal(t, "12", itoa(12))
	assert.Equal(t, "-45", itoa(-45))
}
