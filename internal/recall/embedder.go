// Package recall implements the vector-based long-term memory
// primitive from §9 of docs/IMPLEMENTATION_PLAN_2026_07_16.md. It
// combines an embedder that produces float32 vectors, a SQLite blob
// column that persists them, and a hybrid retriever that reranks a
// cosine-similarity shortlist with the existing FTS5 keyword score.
//
// The store is pure-Go (uses modernc.org/sqlite blobs, no sqlite-vec
// extension) so it works under CGO_ENABLED=0 and on every arch the
// daemon runs on. Retrieval is O(N × D) per query — fine for the
// tens-of-thousands of messages a single-operator daemon accumulates.
// Callers with larger corpora should shard by session_id or migrate to
// a native vector index; the surface is the same either way.
package recall

import (
	"context"
	"errors"
	"fmt"
)

// Embedder produces a fixed-dimension float32 vector for each input
// string. Implementations must be safe for concurrent use.
type Embedder interface {
	// Embed returns one vector per input, in order. Returning fewer
	// vectors than inputs is an error.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dims is the vector dimensionality every returned slice has.
	// Callers verify at construction that the storage layer is sized
	// to match; a runtime mismatch is a bug.
	Dims() int
	// Name is a short identifier logged with every store row so a
	// future migration can detect corpus-wide embedder mixing.
	Name() string
}

// ErrDimensionMismatch signals a caller-supplied vector didn't match
// the embedder's declared Dims().
var ErrDimensionMismatch = errors.New("recall: vector dimension mismatch")

// NoopEmbedder returns a zero-vector for every input. Used when
// embeddings are disabled but calling sites still want to exercise
// the ingest / retrieve paths.
type NoopEmbedder struct{ D int }

// Embed satisfies Embedder.
func (n NoopEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	dims := n.D
	if dims == 0 {
		dims = 4
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, dims)
	}
	return out, nil
}

// Dims satisfies Embedder.
func (n NoopEmbedder) Dims() int {
	if n.D == 0 {
		return 4
	}
	return n.D
}

// Name satisfies Embedder.
func (NoopEmbedder) Name() string { return "noop" }

// ValidateVector returns an error when v is not the expected length.
func ValidateVector(v []float32, expected int) error {
	if len(v) != expected {
		return fmt.Errorf("%w: got %d, want %d", ErrDimensionMismatch, len(v), expected)
	}
	return nil
}
