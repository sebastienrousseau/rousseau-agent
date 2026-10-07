package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A few messages carrying big tool outputs must trigger compression
// on the token estimate even though the message count is tiny.
func TestLLMCompressor_TokenTriggerFiresOnFewLargeMessages(t *testing.T) {
	prov := &stubCompProvider{reply: "summary"}
	c := &LLMCompressor{Provider: prov, TriggerMessages: 60, TriggerTokens: 1000, KeepRecent: 2}
	s := NewSession("x")
	for i := 0; i < 6; i++ {
		s.Append(Message{Role: RoleUser, Content: []Content{
			{Kind: ContentToolResult, ToolResult: &ToolResult{ToolUseID: "t", Output: strings.Repeat("log line\n", 200)}},
		}})
	}
	changed, err := c.Compress(context.Background(), s)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 1, prov.calls)
	assert.Equal(t, 1+2, len(s.Messages))
}

func TestLLMCompressor_TokenTriggerBelowBudgetSkips(t *testing.T) {
	prov := &stubCompProvider{reply: "summary"}
	c := &LLMCompressor{Provider: prov, TriggerTokens: 100_000, KeepRecent: 2}
	s := NewSession("x")
	for i := 0; i < 30; i++ {
		s.Append(NewUserText("short"))
	}
	changed, err := c.Compress(context.Background(), s)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, 0, prov.calls)
}

// A compressed head does not exempt a session that is still over the
// token budget: every pass shrinks the estimate, so re-engaging is
// safe and necessary.
func TestLLMCompressor_TokenTriggerRecompressesCompressedHead(t *testing.T) {
	prov := &stubCompProvider{reply: "tighter"}
	c := &LLMCompressor{Provider: prov, TriggerMessages: 60, TriggerTokens: 500, KeepRecent: 1}
	s := NewSession("x")
	s.Append(Message{Role: RoleUser, Content: []Content{{Kind: ContentText, Text: DefaultCompressorMarker + " " + strings.Repeat("old ", 600)}}})
	for i := 0; i < 3; i++ {
		s.Append(NewUserText(strings.Repeat("new ", 200)))
	}
	changed, err := c.Compress(context.Background(), s)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 2, len(s.Messages))
}

// With no triggers configured Compress is a no-op for size.
func TestLLMCompressor_NoTriggersIsNoop(t *testing.T) {
	prov := &stubCompProvider{reply: "summary"}
	c := &LLMCompressor{Provider: prov, KeepRecent: 1}
	s := NewSession("x")
	for i := 0; i < 100; i++ {
		s.Append(NewUserText(strings.Repeat("x", 1000)))
	}
	changed, err := c.Compress(context.Background(), s)
	require.NoError(t, err)
	assert.False(t, changed)
}
