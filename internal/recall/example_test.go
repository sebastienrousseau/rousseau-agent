package recall_test

import (
	"context"
	"fmt"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/recall"
)

// ExampleRetriever_Recall shows the typical wiring pattern:
// build an in-memory store with a couple of rows, register a
// deterministic embedder, and ask for the top-1 semantic match.
func ExampleRetriever_Recall() {
	store := newExampleStore()
	now := time.Now().UTC()
	_ = store.Put(context.Background(), recall.Row{ //nolint:errcheck // example
		SessionID: "s1", MessageID: 1, Role: "user",
		Text: "the cat sat on the mat", Embedding: []float32{1, 0, 0}, CreatedAt: now,
	})
	_ = store.Put(context.Background(), recall.Row{ //nolint:errcheck // example
		SessionID: "s1", MessageID: 2, Role: "user",
		Text: "the dog barked", Embedding: []float32{0, 1, 0}, CreatedAt: now,
	})

	e := exampleEmbedder{}
	r := recall.NewRetriever(store, e, nil, 1.0)
	hits, _ := r.Recall(context.Background(), "cat", 1) //nolint:errcheck // example
	fmt.Println(hits[0].Text)
	// Output: the cat sat on the mat
}

// exampleEmbedder returns a canned vector for the query "cat" so the
// Example output is stable.
type exampleEmbedder struct{}

func (exampleEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}
func (exampleEmbedder) Dims() int    { return 3 }
func (exampleEmbedder) Name() string { return "example" }

// newExampleStore returns a tiny in-memory Store useful for the
// package's runnable Example.
func newExampleStore() recall.Store {
	return &exampleStore{}
}

type exampleStore struct {
	rows []recall.Row
}

func (s *exampleStore) Put(_ context.Context, r recall.Row) error {
	s.rows = append(s.rows, r)
	return nil
}
func (s *exampleStore) Since(_ context.Context, _ time.Time) ([]recall.Row, error) {
	return s.rows, nil
}
func (s *exampleStore) PurgeOlderThan(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}
