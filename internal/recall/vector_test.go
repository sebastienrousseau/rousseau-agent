package recall

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeVector_RoundTrip(t *testing.T) {
	v := []float32{0.1, -0.2, 3.14, 0, 1e-6}
	b := EncodeVector(v)
	assert.Len(t, b, 4*len(v))
	got, err := DecodeVector(b, len(v))
	require.NoError(t, err)
	assert.Equal(t, v, got)
}

func TestDecodeVector_BadLength(t *testing.T) {
	_, err := DecodeVector([]byte{0x01, 0x02}, 0)
	assert.Error(t, err)
}

func TestDecodeVector_DimMismatch(t *testing.T) {
	v := []float32{1, 2, 3, 4}
	b := EncodeVector(v)
	_, err := DecodeVector(b, 8)
	assert.ErrorIs(t, err, ErrDimensionMismatch)
}

func TestCosineSimilarity_IdenticalIsOne(t *testing.T) {
	v := []float32{1, 2, 3}
	assert.InDelta(t, 1.0, CosineSimilarity(v, v), 1e-5)
}

func TestCosineSimilarity_OrthogonalIsZero(t *testing.T) {
	assert.InDelta(t, 0.0, CosineSimilarity([]float32{1, 0}, []float32{0, 1}), 1e-5)
}

func TestCosineSimilarity_MismatchIsZero(t *testing.T) {
	assert.Equal(t, float32(0), CosineSimilarity([]float32{1, 2}, []float32{1}))
}

func TestCosineSimilarity_ZeroVectorIsZero(t *testing.T) {
	assert.Equal(t, float32(0), CosineSimilarity([]float32{0, 0}, []float32{1, 1}))
}

func TestCosineSimilarity_OppositeIsMinusOne(t *testing.T) {
	got := CosineSimilarity([]float32{1, 0}, []float32{-1, 0})
	assert.InDelta(t, -1.0, float64(got), 1e-5)
}

func TestCosineSimilarity_ReasonableRange(t *testing.T) {
	got := CosineSimilarity([]float32{1, 2, 3}, []float32{4, 5, 6})
	require.Positive(t, float64(got))
	require.LessOrEqual(t, float64(got), 1.0)
	// Manually verify: cos = (1*4 + 2*5 + 3*6) / (sqrt(14) * sqrt(77)).
	expected := 32.0 / (math.Sqrt(14) * math.Sqrt(77))
	assert.InDelta(t, expected, float64(got), 1e-4)
}

func TestChunk_ShortTextIsSingleChunk(t *testing.T) {
	c := Chunk("hello world", 100, 10)
	require.Len(t, c, 1)
	assert.Equal(t, "hello world", c[0])
}

func TestChunk_LongTextSplitsWithOverlap(t *testing.T) {
	words := make([]string, 300)
	for i := range words {
		words[i] = "w"
	}
	text := strings.Join(words, " ")
	c := Chunk(text, 100, 20)
	require.GreaterOrEqual(t, len(c), 3)
	// Every non-final chunk is 100 words.
	for i := 0; i < len(c)-1; i++ {
		assert.Equal(t, 100, len(strings.Fields(c[i])), "chunk %d", i)
	}
}

func TestChunk_EmptyInputYieldsEmptyChunk(t *testing.T) {
	c := Chunk("", 100, 10)
	require.Len(t, c, 1)
	assert.Empty(t, c[0])
}

func TestChunk_OverlapClampedToChunkTokens(t *testing.T) {
	// Overlap ≥ chunkTokens is invalid; the code should snap to a
	// sensible default rather than infinite-loop.
	c := Chunk("one two three four five", 3, 5)
	assert.GreaterOrEqual(t, len(c), 1)
}
