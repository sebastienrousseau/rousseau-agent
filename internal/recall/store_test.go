package recall

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memStore is a tests-only Store implementation.
type memStore struct {
	mu   sync.Mutex
	rows []Row
}

func (m *memStore) Put(_ context.Context, r Row) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, r)
	return nil
}

func (m *memStore) Since(_ context.Context, after time.Time) ([]Row, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Row, 0, len(m.rows))
	for _, r := range m.rows {
		if r.CreatedAt.Before(after) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *memStore) PurgeOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.rows[:0]
	deleted := int64(0)
	for _, r := range m.rows {
		if r.CreatedAt.Before(cutoff) {
			deleted++
			continue
		}
		kept = append(kept, r)
	}
	m.rows = kept
	return deleted, nil
}

// deterministicEmbedder returns the same fixed vector per lowercased
// keyword, so tests can reason about cosine scores. Word "cat" →
// vector [1,0,0]; "dog" → [0,1,0]; "bird" → [0,0,1]; anything else
// → [0.5, 0.5, 0.5].
type deterministicEmbedder struct{}

func (deterministicEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		switch {
		case contains(t, "cat"):
			out[i] = []float32{1, 0, 0}
		case contains(t, "dog"):
			out[i] = []float32{0, 1, 0}
		case contains(t, "bird"):
			out[i] = []float32{0, 0, 1}
		default:
			out[i] = []float32{0.5, 0.5, 0.5}
		}
	}
	return out, nil
}

func (deterministicEmbedder) Dims() int    { return 3 }
func (deterministicEmbedder) Name() string { return "test" }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			c1, c2 := s[i+j], sub[j]
			if c1 >= 'A' && c1 <= 'Z' {
				c1 += 'a' - 'A'
			}
			if c1 != c2 {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestNoopEmbedder_ReturnsCorrectShape(t *testing.T) {
	n := NoopEmbedder{D: 8}
	vecs, err := n.Embed(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, vecs, 2)
	for _, v := range vecs {
		assert.Len(t, v, 8)
	}
	assert.Equal(t, 8, n.Dims())
	assert.Equal(t, "noop", n.Name())
}

func TestValidateVector(t *testing.T) {
	assert.NoError(t, ValidateVector([]float32{1, 2, 3}, 3))
	assert.ErrorIs(t, ValidateVector([]float32{1, 2}, 3), ErrDimensionMismatch)
}

func TestRetriever_ReturnsClosestMatch(t *testing.T) {
	store := &memStore{}
	now := time.Now().UTC()
	rows := []Row{
		{Text: "the cat sat on the mat", Embedding: []float32{1, 0, 0}, CreatedAt: now},
		{Text: "the dog barked", Embedding: []float32{0, 1, 0}, CreatedAt: now},
		{Text: "a small bird chirped", Embedding: []float32{0, 0, 1}, CreatedAt: now},
	}
	for _, r := range rows {
		require.NoError(t, store.Put(context.Background(), r))
	}
	r := NewRetriever(store, deterministicEmbedder{}, nil, 1.0)
	hits, err := r.Recall(context.Background(), "cat behaviour", 1)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Contains(t, hits[0].Text, "cat")
}

func TestRetriever_HybridRerankFavoursKeywordMatch(t *testing.T) {
	store := &memStore{}
	now := time.Now().UTC()
	// Same vector for both rows so keyword breaks the tie.
	rows := []Row{
		{Text: "generic text a", Embedding: []float32{0.5, 0.5, 0.5}, CreatedAt: now},
		{Text: "generic text with cat inside", Embedding: []float32{0.5, 0.5, 0.5}, CreatedAt: now},
	}
	for _, r := range rows {
		require.NoError(t, store.Put(context.Background(), r))
	}
	r := NewRetriever(store, deterministicEmbedder{}, SimpleKeywordScorer, 0.3)
	hits, err := r.Recall(context.Background(), "cat", 1)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Contains(t, hits[0].Text, "cat")
}

func TestRetriever_KEqualsZeroReturnsNothing(t *testing.T) {
	r := NewRetriever(&memStore{}, deterministicEmbedder{}, nil, 1.0)
	hits, err := r.Recall(context.Background(), "cat", 0)
	require.NoError(t, err)
	assert.Empty(t, hits)
}

func TestRetriever_WithWindow(t *testing.T) {
	store := &memStore{}
	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)
	require.NoError(t, store.Put(context.Background(), Row{Text: "old cat message", Embedding: []float32{1, 0, 0}, CreatedAt: old}))
	require.NoError(t, store.Put(context.Background(), Row{Text: "recent dog message", Embedding: []float32{0, 1, 0}, CreatedAt: now}))
	r := NewRetriever(store, deterministicEmbedder{}, nil, 1.0).WithWindow(now.Add(-1 * time.Hour))
	hits, err := r.Recall(context.Background(), "cat", 2)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Contains(t, hits[0].Text, "dog")
}

func TestRetriever_WeightIsClamped(t *testing.T) {
	r := NewRetriever(&memStore{}, deterministicEmbedder{}, SimpleKeywordScorer, 5.0)
	assert.EqualValues(t, 1.0, r.weight)
	r = NewRetriever(&memStore{}, deterministicEmbedder{}, SimpleKeywordScorer, -0.5)
	assert.EqualValues(t, 0.0, r.weight)
}

func TestIngester_PushEmbedsAndPersists(t *testing.T) {
	store := &memStore{}
	i := NewIngester(store, deterministicEmbedder{}, IngesterConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	i.Start(context.Background())
	defer i.Stop()

	i.Push("s1", 1, "user", "hello cat", time.Now())

	// Poll for the worker to drain.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		n := len(store.rows)
		store.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	require.NotEmpty(t, store.rows)
	assert.Equal(t, "hello cat", store.rows[0].Text)
	assert.NotEmpty(t, store.rows[0].Embedding)
	assert.Equal(t, "test", store.rows[0].Embedder)
}

func TestIngester_QueueOverflowDropsOldest(t *testing.T) {
	// A blocking embedder so we can pile up jobs faster than the
	// worker drains them.
	blocker := make(chan struct{})
	e := blockingEmbedder{gate: blocker}
	store := &memStore{}
	i := NewIngester(store, e, IngesterConfig{MaxQueue: 3},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	i.Start(context.Background())
	defer func() {
		close(blocker) // let worker finish before Stop drains.
		i.Stop()
	}()

	for k := 0; k < 20; k++ {
		i.Push("s1", int64(k), "user", "message", time.Now())
	}
	// Give queue a moment to reach steady state.
	time.Sleep(20 * time.Millisecond)
	assert.LessOrEqual(t, i.QueueDepth(), 3)
	assert.Positive(t, i.DroppedCount())
}

type blockingEmbedder struct {
	gate chan struct{}
}

func (b blockingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case <-b.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{0, 0, 0}
	}
	return out, nil
}
func (blockingEmbedder) Dims() int    { return 3 }
func (blockingEmbedder) Name() string { return "blocker" }

func TestVoyageEmbedder_RejectsMissingKey(t *testing.T) {
	t.Setenv(EnvVoyageAPIKey, "")
	_, err := NewVoyageEmbedder(VoyageConfig{})
	assert.Error(t, err)
}

func TestVoyageEmbedder_KnownModelDims(t *testing.T) {
	e, err := NewVoyageEmbedder(VoyageConfig{APIKey: "k"})
	require.NoError(t, err)
	assert.Equal(t, 1024, e.Dims())
	assert.Equal(t, "voyage:voyage-3-lite", e.Name())
}

func TestVoyageEmbedder_UnknownModelRequiresDims(t *testing.T) {
	_, err := NewVoyageEmbedder(VoyageConfig{APIKey: "k", Model: "unknown-model"})
	assert.Error(t, err)

	e, err := NewVoyageEmbedder(VoyageConfig{APIKey: "k", Model: "unknown-model", Dims: 512})
	require.NoError(t, err)
	assert.Equal(t, 512, e.Dims())
}

func TestSimpleKeywordScorer(t *testing.T) {
	// All three query terms appear.
	assert.InDelta(t, 1.0, SimpleKeywordScorer("cat dog bird", "a cat and a dog and a bird"), 0.01)
	// None appear.
	assert.InDelta(t, 0.0, SimpleKeywordScorer("cat", "nothing"), 0.01)
	// Two of three appear.
	assert.InDelta(t, 2.0/3.0, SimpleKeywordScorer("cat dog bird", "cat and dog only"), 0.01)
}

func TestSimpleKeywordScorer_EmptyQueryReturnsZero(t *testing.T) {
	assert.Equal(t, float32(0), SimpleKeywordScorer("", "any text"))
}
