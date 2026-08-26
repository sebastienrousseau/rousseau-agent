package recall

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqlitestate "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// -- test doubles -------------------------------------------------------

// failingEmbedder always fails, exercising the "embedding backend is
// down" path.
type failingEmbedder struct{ err error }

func (f failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, f.err
}
func (failingEmbedder) Dims() int    { return 3 }
func (failingEmbedder) Name() string { return "failing" }

// emptyEmbedder returns no vectors at all — a well-formed but useless
// response some providers give for filtered input.
type emptyEmbedder struct{}

func (emptyEmbedder) Embed(context.Context, []string) ([][]float32, error) { return nil, nil }
func (emptyEmbedder) Dims() int                                            { return 3 }
func (emptyEmbedder) Name() string                                         { return "empty" }

// scriptedStore lets a test choose what Since/Put do. The ingester
// calls it from its worker goroutine, so the counter is guarded.
type scriptedStore struct {
	mu      sync.Mutex
	rows    []Row
	sinceEr error
	putErr  error
	puts    int
}

func (s *scriptedStore) Put(context.Context, Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	return s.putErr
}

func (s *scriptedStore) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

func (s *scriptedStore) Since(context.Context, time.Time) ([]Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sinceEr != nil {
		return nil, s.sinceEr
	}
	return s.rows, nil
}

func (s *scriptedStore) PurgeOlderThan(context.Context, time.Time) (int64, error) { return 0, nil }

// syncBuffer is a concurrency-safe log sink: the ingester logs from
// its worker goroutine while the test reads from its own.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// capturingLogger returns a logger plus the buffer it writes to.
func capturingLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// roundTripFunc adapts a function to http.RoundTripper so transport
// failures can be produced without a network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// -- Chunk / tokenize defaults ------------------------------------------

func TestChunk_NonPositiveTokenBudgetFallsBackToDefault(t *testing.T) {
	// 500 words with chunkTokens<=0 must fall back to the 400-token
	// default, which splits the text.
	words := make([]string, 500)
	for i := range words {
		words[i] = "word"
	}
	text := joinWords(words)

	for _, tokens := range []int{0, -7} {
		got := Chunk(text, tokens, 40)
		assert.Greater(t, len(got), 1, "chunkTokens=%d should use the 400-token default", tokens)
	}
}

func TestChunk_NegativeOverlapFallsBackToDefault(t *testing.T) {
	words := make([]string, 300)
	for i := range words {
		words[i] = "w"
	}
	text := joinWords(words)

	// chunkTokens=100 with the default overlap of 40 gives step=60.
	got := Chunk(text, 100, -1)
	assert.Len(t, got, 5)
}

func TestSimpleKeywordScorer_DeduplicatesRepeatedQueryTerms(t *testing.T) {
	// "go go go" tokenises to a single unique term, so a text
	// containing it scores a full 1 rather than 1/3.
	assert.InDelta(t, 1.0, float64(SimpleKeywordScorer("go go go", "go routines")), 1e-6)
	assert.InDelta(t, 0.5, float64(SimpleKeywordScorer("go go rust", "go routines")), 1e-6)
}

// -- Retriever error paths ----------------------------------------------

func TestRetriever_PropagatesEmbedderFailure(t *testing.T) {
	boom := errors.New("embedder offline")
	r := NewRetriever(&scriptedStore{}, failingEmbedder{err: boom}, nil, 0.7)

	hits, err := r.Recall(context.Background(), "anything", 5)
	assert.ErrorIs(t, err, boom)
	assert.Nil(t, hits)
}

func TestRetriever_EmptyEmbeddingResponseYieldsNoHits(t *testing.T) {
	store := &scriptedStore{rows: []Row{{ID: 1, Embedding: []float32{1, 0, 0}}}}
	r := NewRetriever(store, emptyEmbedder{}, nil, 0.7)

	hits, err := r.Recall(context.Background(), "anything", 5)
	require.NoError(t, err)
	assert.Nil(t, hits)
}

func TestRetriever_PropagatesStoreFailure(t *testing.T) {
	boom := errors.New("store unavailable")
	r := NewRetriever(&scriptedStore{sinceEr: boom}, NoopEmbedder{D: 3}, nil, 0.7)

	hits, err := r.Recall(context.Background(), "anything", 5)
	assert.ErrorIs(t, err, boom)
	assert.Nil(t, hits)
}

func TestRetriever_EmptyCorpusYieldsNoHits(t *testing.T) {
	r := NewRetriever(&scriptedStore{}, NoopEmbedder{D: 3}, nil, 0.7)

	hits, err := r.Recall(context.Background(), "anything", 5)
	require.NoError(t, err)
	assert.Nil(t, hits)
}

func TestRetriever_SkipsRowsWithoutEmbeddings(t *testing.T) {
	store := &scriptedStore{rows: []Row{
		{ID: 1, Text: "no vector", Embedding: nil},
		{ID: 2, Text: "has vector", Embedding: []float32{1, 0, 0}},
	}}
	r := NewRetriever(store, deterministicEmbedder{}, nil, 1.0)

	hits, err := r.Recall(context.Background(), "has vector", 5)
	require.NoError(t, err)
	require.Len(t, hits, 1, "unembedded rows must not be scored")
	assert.Equal(t, int64(2), hits[0].ID)
}

// -- Ingester error paths -----------------------------------------------

func TestNewIngester_NegativeOverlapFallsBackToDefault(t *testing.T) {
	i := NewIngester(&scriptedStore{}, NoopEmbedder{D: 3},
		IngesterConfig{ChunkTokens: 100, ChunkOverlap: -5}, nil)
	assert.Equal(t, 40, i.chunkOverlap)
}

func TestIngester_ContextCancellationStopsWorker(t *testing.T) {
	i := NewIngester(&scriptedStore{}, NoopEmbedder{D: 3}, IngesterConfig{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	i.Start(ctx)

	cancel()
	select {
	case <-i.done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after context cancellation")
	}
}

func TestIngester_EmbedFailureAbandonsBatchWithoutWriting(t *testing.T) {
	store := &scriptedStore{}
	logger, logs := capturingLogger()
	i := NewIngester(store, failingEmbedder{err: errors.New("429 rate limited")},
		IngesterConfig{BatchSize: 4}, logger)
	i.Start(context.Background())

	i.Push("sess", 1, "user", "some text to embed", time.Now())
	waitFor(t, func() bool { return strings.Contains(logs.String(), "recall.embed_failed") })
	i.Stop()

	assert.Contains(t, logs.String(), "recall.embed_failed")
	assert.Contains(t, logs.String(), "429 rate limited")
	assert.Zero(t, store.putCount(), "a failed embed must not reach the store")
}

func TestIngester_StoreFailureIsLoggedAndDoesNotStallTheWorker(t *testing.T) {
	store := &scriptedStore{putErr: errors.New("disk full")}
	logger, logs := capturingLogger()
	i := NewIngester(store, NoopEmbedder{D: 3}, IngesterConfig{BatchSize: 4}, logger)
	i.Start(context.Background())

	i.Push("sess", 1, "user", "first", time.Now())
	i.Push("sess", 2, "user", "second", time.Now())
	i.Stop()

	assert.Contains(t, logs.String(), "recall.put_failed")
	assert.Contains(t, logs.String(), "disk full")
	assert.Equal(t, 2, store.putCount(), "the worker keeps going after a write failure")
	assert.Zero(t, i.QueueDepth())
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// -- SQLiteStore --------------------------------------------------------

func TestSQLiteStore_SincePropagatesStoreFailure(t *testing.T) {
	ctx := context.Background()
	st, err := sqlitestate.Open(ctx, filepath.Join(t.TempDir(), "recall.db"))
	require.NoError(t, err)
	rv, err := sqlitestate.NewRecallVectors(ctx, st)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	rows, err := NewSQLiteStore(rv, 3).Since(ctx, time.Time{})
	assert.Error(t, err)
	assert.Nil(t, rows)
}

// -- VoyageEmbedder error paths -----------------------------------------

func TestVoyageEmbedder_RejectsUnbuildableRequestURL(t *testing.T) {
	e, err := NewVoyageEmbedder(VoyageConfig{
		APIKey:  "k",
		Dims:    3,
		BaseURL: "http://exa\x7fmple.invalid",
	})
	require.NoError(t, err)

	vecs, err := e.Embed(context.Background(), []string{"hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "build request")
	assert.Nil(t, vecs)
}

func TestVoyageEmbedder_SurfacesTransportFailure(t *testing.T) {
	boom := errors.New("dial refused")
	e, err := NewVoyageEmbedder(VoyageConfig{
		APIKey:  "k",
		Dims:    3,
		BaseURL: "http://voyage.invalid/v1",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, boom
		})},
	})
	require.NoError(t, err)

	vecs, err := e.Embed(context.Background(), []string{"hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "voyage: post")
	assert.Nil(t, vecs)
}

func TestVoyageEmbedder_RejectsOutOfRangeResponseIndex(t *testing.T) {
	for name, body := range map[string]string{
		"too high": `{"data":[{"index":7,"embedding":[1,2,3]}]}`,
		"negative": `{"data":[{"index":-1,"embedding":[1,2,3]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			e, err := NewVoyageEmbedder(VoyageConfig{
				APIKey:  "k",
				Dims:    3,
				BaseURL: "http://voyage.invalid/v1",
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(body)),
						Header:     http.Header{},
					}, nil
				})},
			})
			require.NoError(t, err)

			vecs, err := e.Embed(context.Background(), []string{"one"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "bad response index")
			assert.Nil(t, vecs)
		})
	}
}
