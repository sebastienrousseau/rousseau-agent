package recall

import (
	"math/rand"
	"testing"
)

// BenchmarkCosineSimilarity_1024D measures the per-vector cost at
// voyage-3-lite's dimensionality — the dominant term of retrieval
// latency (retrieval is O(N × D) cosine).
func BenchmarkCosineSimilarity_1024D(b *testing.B) {
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic bench
	a := make([]float32, 1024)
	c := make([]float32, 1024)
	for i := range a {
		a[i] = rng.Float32()
		c[i] = rng.Float32()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CosineSimilarity(a, c)
	}
}

// BenchmarkEncodeDecodeVector_1024D measures the storage-boundary
// round-trip cost. Every ingest + retrieve touches this.
func BenchmarkEncodeDecodeVector_1024D(b *testing.B) {
	rng := rand.New(rand.NewSource(2)) //nolint:gosec // deterministic bench
	v := make([]float32, 1024)
	for i := range v {
		v[i] = rng.Float32()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		enc := EncodeVector(v)
		if _, err := DecodeVector(enc, 1024); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkChunk_400Tokens measures Chunk on a mid-sized document.
// Every ingest event pays this cost.
func BenchmarkChunk_400Tokens(b *testing.B) {
	// Build a ~2000-word document.
	words := make([]byte, 0, 12000)
	for i := 0; i < 2000; i++ {
		words = append(words, 'w', 'o', 'r', 'd', ' ')
	}
	text := string(words)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Chunk(text, 400, 40)
	}
}
