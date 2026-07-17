package recall

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqlitestate "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// writeVoyageBody writes a canned two-vector response — kept out
// of the test body so //nolint tags line up cleanly.
func writeVoyageBody(t *testing.T, w io.Writer) {
	t.Helper()
	body := `{"data":[{"index":0,"embedding":[0.1,0.2,0.3]},{"index":1,"embedding":[0.4,0.5,0.6]}]}`
	_, err := w.Write([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
}

// TestSQLiteStore_PutSinceRoundTrip drives the sqlite-backed
// adapter end-to-end so the coverage report shows the thin wrapper
// runs its own bytes rather than trusting the underlying tests.
func TestSQLiteStore_PutSinceRoundTrip(t *testing.T) {
	s, err := sqlitestate.Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup
	inner, err := sqlitestate.NewRecallVectors(context.Background(), s)
	require.NoError(t, err)

	store := NewSQLiteStore(inner, 3)
	now := time.Now().UTC()
	row := Row{
		SessionID: "s1", MessageID: 1, ChunkIndex: 0,
		Role: "user", Text: "hi",
		Embedding: []float32{1, 0, 0}, CreatedAt: now,
		Embedder: "test",
	}
	require.NoError(t, store.Put(context.Background(), row))

	// Wrong dims → error, no row written twice.
	assert.ErrorIs(t, store.Put(context.Background(),
		Row{SessionID: "s1", MessageID: 2, Embedding: []float32{1, 0}, CreatedAt: now}),
		ErrDimensionMismatch)

	rows, err := store.Since(context.Background(), now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.InDelta(t, float32(1.0), rows[0].Embedding[0], 1e-5)
}

func TestSQLiteStore_PurgeOlderThan(t *testing.T) {
	s, err := sqlitestate.Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup
	inner, err := sqlitestate.NewRecallVectors(context.Background(), s)
	require.NoError(t, err)
	store := NewSQLiteStore(inner, 3)
	old := time.Now().UTC().Add(-48 * time.Hour)
	require.NoError(t, store.Put(context.Background(), Row{
		SessionID: "s1", MessageID: 1, Embedding: []float32{1, 0, 0}, CreatedAt: old,
	}))
	n, err := store.PurgeOlderThan(context.Background(), time.Now().UTC().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}

// TestSQLiteStore_CrossEmbedderRowsSkipped verifies the adapter
// drops rows whose vector length doesn't match the configured dims —
// a config change must not corrupt retrieval.
func TestSQLiteStore_CrossEmbedderRowsSkipped(t *testing.T) {
	s, err := sqlitestate.Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup
	inner, err := sqlitestate.NewRecallVectors(context.Background(), s)
	require.NoError(t, err)

	// Insert a raw row via the sqlite layer with a wrong-shaped
	// vector.
	require.NoError(t, inner.Put(context.Background(), sqlitestate.VectorRow{
		SessionID: "s1", MessageID: 1, ChunkIndex: 0,
		Role: "u", Text: "old",
		Embedding: EncodeVector([]float32{1, 0}), // 2 dims
		CreatedAt: time.Now().UTC(),
		Embedder:  "old-cfg",
	}))

	store := NewSQLiteStore(inner, 3)
	rows, err := store.Since(context.Background(), time.Time{})
	require.NoError(t, err)
	assert.Empty(t, rows, "cross-embedder rows must be silently dropped")
}

// TestVoyageEmbedder_Embed drives the wire protocol with a fake
// endpoint so the Voyage adapter's HTTP path stops showing as 0%.
func TestVoyageEmbedder_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/embeddings", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		assert.Equal(t, "voyage-3-lite", req.Model)
		assert.Len(t, req.Input, 2)
		writeVoyageBody(t, w)
	}))
	defer srv.Close()

	e, err := NewVoyageEmbedder(VoyageConfig{
		APIKey: "test-key", BaseURL: srv.URL, HTTPClient: srv.Client(), Dims: 3,
	})
	require.NoError(t, err)
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, vecs, 2)
	assert.InDelta(t, float32(0.1), vecs[0][0], 1e-4)
	assert.InDelta(t, float32(0.6), vecs[1][2], 1e-4)
}

func TestVoyageEmbedder_EmbedEmptyInput(t *testing.T) {
	e, err := NewVoyageEmbedder(VoyageConfig{APIKey: "k"})
	require.NoError(t, err)
	vecs, err := e.Embed(context.Background(), nil)
	require.NoError(t, err)
	assert.Nil(t, vecs)
}

func TestVoyageEmbedder_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid_key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	e, err := NewVoyageEmbedder(VoyageConfig{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client(), Dims: 3})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	assert.ErrorContains(t, err, "401")
}

func TestVoyageEmbedder_BadJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	e, err := NewVoyageEmbedder(VoyageConfig{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client(), Dims: 3})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	assert.ErrorContains(t, err, "decode")
}

func TestVoyageEmbedder_WrongCountResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1]}]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	e, err := NewVoyageEmbedder(VoyageConfig{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client(), Dims: 1})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a", "b"})
	assert.ErrorContains(t, err, "expected 2")
}

func TestVoyageModelDims_AllKnown(t *testing.T) {
	for _, m := range []string{"voyage-3-lite", "voyage-3", "voyage-3-large", "voyage-code-3"} {
		assert.Positive(t, voyageModelDims(m), m)
	}
	assert.Zero(t, voyageModelDims("unknown-model"))
}

func TestNoopEmbedder_DefaultDims(t *testing.T) {
	n := NoopEmbedder{}
	assert.Equal(t, 4, n.Dims())
	vecs, err := n.Embed(context.Background(), []string{"a"})
	require.NoError(t, err)
	require.Len(t, vecs, 1)
	assert.Len(t, vecs[0], 4)
}

func TestIngester_StopDrainsQueueGracefully(t *testing.T) {
	store := &memStore{}
	e := deterministicEmbedder{}
	i := NewIngester(store, e, IngesterConfig{}, nil)
	i.Start(context.Background())
	i.Push("s1", 1, "user", "cat", time.Now())
	i.Push("s1", 2, "user", "dog", time.Now())
	// Give a moment for drain to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if store.count() >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	i.Stop()
	assert.GreaterOrEqual(t, store.count(), 2)
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func TestChunk_LargerThanDoc(t *testing.T) {
	// A chunkTokens larger than the input word count should return
	// the whole text as one chunk.
	c := Chunk("only three words", 100, 10)
	require.Len(t, c, 1)
	assert.Equal(t, "only three words", c[0])
}

func TestTokenize_HandlesUnicode(t *testing.T) {
	toks := tokenize("café résumé naïve")
	// Unicode letters should tokenise as one word each — assert non-
	// empty and non-duplicative.
	assert.NotEmpty(t, toks)
	seen := map[string]bool{}
	for _, tok := range toks {
		assert.False(t, seen[tok], "duplicate token %q", tok)
		seen[tok] = true
	}
}

func TestSimpleKeywordScorer_CaseInsensitive(t *testing.T) {
	score := SimpleKeywordScorer("Cat", "the CAT sat")
	assert.InDelta(t, 1.0, score, 0.01)
}

func TestNewVoyageEmbedder_ExplicitBaseURL(t *testing.T) {
	e, err := NewVoyageEmbedder(VoyageConfig{APIKey: "k", BaseURL: "http://x"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(e.baseURL, "http://x"))
}
