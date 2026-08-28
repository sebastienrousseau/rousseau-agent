package vertex

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

func TestToVertexContent_Image(t *testing.T) {
	raw := []byte{0x89, 0x50, 0x4E, 0x47}
	msg := agent.NewUserImage("image/png", raw, "test")
	blocks, err := toVertexContent(msg.Content)
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

func TestToVertexContent_NilImageRejected(t *testing.T) {
	_, err := toVertexContent([]agent.Content{{Kind: agent.ContentImage}})
	assert.ErrorContains(t, err, "image content missing payload")
}
