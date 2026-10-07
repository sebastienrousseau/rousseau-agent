package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEstimateTokens(t *testing.T) {
	assert.Equal(t, 0, EstimateTokens(nil))

	text := NewUserText(strings.Repeat("a", 400))
	assert.Equal(t, (400+16)/4, EstimateTokens([]Message{text}))

	tool := Message{Role: RoleAssistant, Content: []Content{
		{Kind: ContentToolUse, ToolUse: &ToolUse{Name: "read", Input: json.RawMessage(strings.Repeat("x", 100))}},
	}}
	result := Message{Role: RoleUser, Content: []Content{
		{Kind: ContentToolResult, ToolResult: &ToolResult{Output: strings.Repeat("y", 300)}},
	}}
	assert.Equal(t, (4+100+16+300+16)/4, EstimateTokens([]Message{tool, result}))

	img := NewUserImage("image/png", make([]byte, 50_000), "x")
	assert.Equal(t, 16/4+tokensPerImage, EstimateTokens([]Message{img}), "images are a flat charge, not their byte size")
}
