package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

func TestUserMessage_TextOnlyIsPlainString(t *testing.T) {
	m := userMessage([]agent.Content{{Kind: agent.ContentText, Text: "hi"}})
	// Serialise and confirm the shape is the string variant.
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"content":"hi"`)
}

func TestUserMessage_WithImageEmitsParts(t *testing.T) {
	m := userMessage([]agent.Content{
		{Kind: agent.ContentText, Text: "what's this"},
		{Kind: agent.ContentImage, Image: &agent.Image{
			MediaType: "image/png",
			Data:      []byte{0x89, 0x50, 0x4E, 0x47},
			Source:    "whatsapp",
		}},
	})
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	// The multipart variant emits a JSON array under "content".
	assert.Contains(t, string(raw), `"role":"user"`)
	assert.True(t, strings.Contains(string(raw), `data:image/png;base64,`), "raw=%s", raw)
	assert.Contains(t, string(raw), "what's this")
}

func TestUserMessage_ImageOnlyStillEmitsParts(t *testing.T) {
	m := userMessage([]agent.Content{
		{Kind: agent.ContentImage, Image: &agent.Image{
			MediaType: "image/jpeg", Data: []byte{0xFF, 0xD8},
		}},
	})
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `data:image/jpeg;base64,`)
}
