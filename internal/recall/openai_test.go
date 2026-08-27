package recall

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeOpenAIBody writes the wire-shape OpenAI returns for a batch of
// two three-dimensional embeddings. Test-only fixture.
func writeOpenAIBody(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	payload := map[string]any{
		"data": []map[string]any{
			{"index": 0, "embedding": []float32{0.1, 0.2, 0.3}},
			{"index": 1, "embedding": []float32{0.4, 0.5, 0.6}},
		},
	}
	blob, err := json.Marshal(payload)
	require.NoError(t, err)
	_, err = w.Write(blob)
	require.NoError(t, err)
}

func TestOpenAIEmbedder_RequiresAPIKeyAcrossFallbacks(t *testing.T) {
	// Both env vars must be empty to trip the "API key required"
	// path. We can't reliably unset them in-process, so set them to
	// empty via t.Setenv and let the constructor error surface.
	t.Setenv(EnvOpenAIAPIKey, "")
	t.Setenv(EnvOpenAIAPIKeyFallback, "")
	_, err := NewOpenAIEmbedder(OpenAIConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), EnvOpenAIAPIKey)
}

func TestOpenAIEmbedder_PicksUpScopedEnvKey(t *testing.T) {
	t.Setenv(EnvOpenAIAPIKey, "scoped-key")
	t.Setenv(EnvOpenAIAPIKeyFallback, "") // ensure scoped variant wins
	e, err := NewOpenAIEmbedder(OpenAIConfig{})
	require.NoError(t, err)
	assert.Equal(t, "scoped-key", e.apiKey)
}

func TestOpenAIEmbedder_FallsBackToStandardEnvKey(t *testing.T) {
	t.Setenv(EnvOpenAIAPIKey, "")
	t.Setenv(EnvOpenAIAPIKeyFallback, "stock-key")
	e, err := NewOpenAIEmbedder(OpenAIConfig{})
	require.NoError(t, err)
	assert.Equal(t, "stock-key", e.apiKey)
}

func TestOpenAIEmbedder_DefaultsModelAndDims(t *testing.T) {
	e, err := NewOpenAIEmbedder(OpenAIConfig{APIKey: "k"})
	require.NoError(t, err)
	assert.Equal(t, "text-embedding-3-small", e.model)
	assert.Equal(t, 1536, e.Dims())
	assert.Equal(t, "openai:text-embedding-3-small", e.Name())
}

func TestOpenAIEmbedder_UnknownModelRequiresDims(t *testing.T) {
	_, err := NewOpenAIEmbedder(OpenAIConfig{APIKey: "k", Model: "some-unreleased-model"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "supply Dims")
}

func TestOpenAIEmbedder_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/embeddings", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		var req struct {
			Input          []string `json:"input"`
			Model          string   `json:"model"`
			EncodingFormat string   `json:"encoding_format"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		assert.Equal(t, "text-embedding-3-small", req.Model)
		assert.Equal(t, "float", req.EncodingFormat)
		assert.Len(t, req.Input, 2)
		writeOpenAIBody(t, w)
	}))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(OpenAIConfig{
		APIKey: "test-key", BaseURL: srv.URL, HTTPClient: srv.Client(), Dims: 3,
	})
	require.NoError(t, err)
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, vecs, 2)
	assert.InDelta(t, float32(0.1), vecs[0][0], 1e-4)
	assert.InDelta(t, float32(0.6), vecs[1][2], 1e-4)
}

func TestOpenAIEmbedder_EmbedEmptyInput(t *testing.T) {
	e, err := NewOpenAIEmbedder(OpenAIConfig{APIKey: "k"})
	require.NoError(t, err)
	vecs, err := e.Embed(context.Background(), nil)
	require.NoError(t, err)
	assert.Nil(t, vecs)
}

func TestOpenAIEmbedder_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid_api_key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	e, err := NewOpenAIEmbedder(OpenAIConfig{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client(), Dims: 3})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	assert.ErrorContains(t, err, "401")
}

func TestOpenAIEmbedder_BadJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	e, err := NewOpenAIEmbedder(OpenAIConfig{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client(), Dims: 3})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	assert.ErrorContains(t, err, "decode")
}

func TestOpenAIEmbedder_WrongCountResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1]}]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	e, err := NewOpenAIEmbedder(OpenAIConfig{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client(), Dims: 1})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a", "b"})
	assert.ErrorContains(t, err, "expected 2")
}

func TestOpenAIEmbedder_WrongDimsInResponse(t *testing.T) {
	// The model returned 2-dim vectors but the embedder was configured
	// with 3 dims — surfaced explicitly so an operator does not end up
	// with silently-truncated storage.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	e, err := NewOpenAIEmbedder(OpenAIConfig{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client(), Dims: 3})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	assert.ErrorContains(t, err, "2 dims")
}

func TestOpenAIEmbedder_OutOfRangeIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":9,"embedding":[0.1,0.2,0.3]}]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	e, err := NewOpenAIEmbedder(OpenAIConfig{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client(), Dims: 3})
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	assert.ErrorContains(t, err, "bad response index")
}

func TestOpenAIModelDims_AllKnown(t *testing.T) {
	for _, m := range []string{
		"text-embedding-3-small",
		"text-embedding-3-large",
		"text-embedding-ada-002",
	} {
		assert.Positive(t, openAIModelDims(m), m)
	}
	assert.Zero(t, openAIModelDims("unknown-model"))
}
