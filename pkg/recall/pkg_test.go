package recall_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/pkg/recall"
)

func TestFacadeShape(t *testing.T) {
	// NoopEmbedder + vector helpers reach through the façade.
	e := recall.NoopEmbedder{D: 4}
	assert.Equal(t, 4, e.Dims())

	v := []float32{1, 2, 3, 4}
	assert.InDelta(t, 1.0, recall.CosineSimilarity(v, v), 1e-5)

	enc := recall.EncodeVector(v)
	dec, err := recall.DecodeVector(enc, 4)
	assert.NoError(t, err)
	assert.Equal(t, v, dec)

	chunks := recall.Chunk("hello world one two three", 3, 1)
	assert.NotEmpty(t, chunks)
}
