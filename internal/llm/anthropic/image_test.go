package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestToSDKContent_ImageBlock verifies that an agent.ContentImage
// block flows through toSDKContent into the SDK's image-block wire
// shape without truncation.
func TestToSDKContent_ImageBlock(t *testing.T) {
	msg := agent.NewUserImage("image/png", []byte{0x89, 0x50, 0x4E, 0x47}, "test")
	blocks, err := toSDKContent(msg.Content)
	require.NoError(t, err)
	require.Len(t, blocks, 1)

	// Round-trip through JSON to inspect the wire shape without
	// coupling to SDK internals.
	raw, err := json.Marshal(blocks[0])
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(raw, &envelope))
	assert.Equal(t, "image", envelope["type"])
	source, ok := envelope["source"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "base64", source["type"])
	assert.Equal(t, "image/png", source["media_type"])
	assert.NotEmpty(t, source["data"])
}

func TestToSDKContent_NilImageRejected(t *testing.T) {
	_, err := toSDKContent([]agent.Content{{Kind: agent.ContentImage}})
	assert.ErrorContains(t, err, "image content missing payload")
}
