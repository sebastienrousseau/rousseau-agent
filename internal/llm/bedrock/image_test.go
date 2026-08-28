package bedrock

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestToBedrockContent_Image asserts the type:"image" wire shape
// with a base64-encoded source.
func TestToBedrockContent_Image(t *testing.T) {
	raw := []byte{0x89, 0x50, 0x4E, 0x47}
	msg := agent.NewUserImage("image/png", raw, "test")
	blocks, err := toBedrockContent(msg.Content)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	assert.Equal(t, "image", blocks[0].Type)
	require.NotNil(t, blocks[0].Source)
	assert.Equal(t, "base64", blocks[0].Source.Type)
	assert.Equal(t, "image/png", blocks[0].Source.MediaType)
	decoded, err := base64.StdEncoding.DecodeString(blocks[0].Source.Data)
	require.NoError(t, err)
	assert.Equal(t, raw, decoded)
}

func TestToBedrockContent_NilImageRejected(t *testing.T) {
	_, err := toBedrockContent([]agent.Content{{Kind: agent.ContentImage}})
	assert.ErrorContains(t, err, "image content missing payload")
}
