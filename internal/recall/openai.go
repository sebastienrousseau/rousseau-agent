package recall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// OpenAIEmbedder calls the OpenAI `/embeddings` endpoint. Default
// model is text-embedding-3-small (1536 dims) — the cheapest quality
// tier and the one §9.2 of docs/IMPLEMENTATION_PLAN_2026_07_16.md
// names as the OpenAI baseline. The transport shape mirrors
// VoyageEmbedder so every operator swap is a config-string change,
// not a code change.
type OpenAIEmbedder struct {
	apiKey  string
	model   string
	dims    int
	http    *http.Client
	baseURL string
}

// OpenAIConfig configures an OpenAIEmbedder.
type OpenAIConfig struct {
	// APIKey — OpenAI API key. Empty falls back to
	// $ROUSSEAU_OPENAI_API_KEY then $OPENAI_API_KEY. The rousseau-
	// prefixed variant lets an operator keep separate keys for the
	// LLM provider and the embedder without colliding with a stock
	// OPENAI_API_KEY export.
	APIKey string
	// Model — one of OpenAI's supported embedding model IDs. Empty
	// uses "text-embedding-3-small".
	Model string
	// Dims — the model's vector dimensionality. Required unless Model
	// is one of the well-known defaults handled by openAIModelDims.
	Dims int
	// BaseURL — override for tests and OpenAI-compatible endpoints
	// (Ollama, LM Studio, Together AI). Empty uses
	// https://api.openai.com/v1.
	BaseURL string
	// HTTPClient — injected in tests. Empty uses a 30s-timeout client.
	HTTPClient *http.Client
}

// EnvOpenAIAPIKey is the rousseau-scoped env fallback for
// OpenAIConfig.APIKey. Preferred over the shared OPENAI_API_KEY so an
// operator can point rousseau at a dedicated embedding budget.
const EnvOpenAIAPIKey = "ROUSSEAU_OPENAI_API_KEY"

// EnvOpenAIAPIKeyFallback is the shared OpenAI env var. Checked when
// the scoped variant is not set — most operators only export the
// stock variable, and refusing to use it would be needlessly hostile.
const EnvOpenAIAPIKeyFallback = "OPENAI_API_KEY"

// NewOpenAIEmbedder constructs an OpenAI-backed embedder.
func NewOpenAIEmbedder(cfg OpenAIConfig) (*OpenAIEmbedder, error) {
	key := cfg.APIKey
	if key == "" {
		key = os.Getenv(EnvOpenAIAPIKey)
	}
	if key == "" {
		key = os.Getenv(EnvOpenAIAPIKeyFallback)
	}
	if key == "" {
		return nil, fmt.Errorf("openai: API key required (set $%s, $%s, or Config.APIKey)",
			EnvOpenAIAPIKey, EnvOpenAIAPIKeyFallback)
	}
	model := cfg.Model
	if model == "" {
		model = "text-embedding-3-small"
	}
	dims := cfg.Dims
	if dims == 0 {
		dims = openAIModelDims(model)
	}
	if dims == 0 {
		return nil, fmt.Errorf("openai: unknown model %q — supply Dims explicitly", model)
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	h := cfg.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	return &OpenAIEmbedder{apiKey: key, model: model, dims: dims, http: h, baseURL: base}, nil
}

// Embed satisfies Embedder. Sends one batched POST — OpenAI accepts
// up to 2048 inputs per request, plenty for the tens-of-thousands of
// messages a single-operator daemon accumulates.
func (o *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{
		"input":           texts,
		"model":           o.model,
		"encoding_format": "float",
	})
	if err != nil {
		return nil, fmt.Errorf("openai: marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024)) //nolint:errcheck // best-effort error body
		return nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, string(snippet))
	}

	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("openai: expected %d embeddings, got %d", len(texts), len(out.Data))
	}
	vecs := make([][]float32, len(texts))
	for _, row := range out.Data {
		if row.Index < 0 || row.Index >= len(vecs) {
			return nil, fmt.Errorf("openai: bad response index %d", row.Index)
		}
		if len(row.Embedding) != o.dims {
			return nil, fmt.Errorf("openai: model returned %d dims, configured %d", len(row.Embedding), o.dims)
		}
		vecs[row.Index] = row.Embedding
	}
	return vecs, nil
}

// Dims satisfies Embedder.
func (o *OpenAIEmbedder) Dims() int { return o.dims }

// Name satisfies Embedder. Prefixed with "openai:" so the persisted
// row can distinguish text-embedding-3-small from voyage-3-lite in a
// mixed-corpus deployment.
func (o *OpenAIEmbedder) Name() string { return "openai:" + o.model }

// openAIModelDims maps well-known OpenAI embedding model IDs to their
// vector dimensionality so operators do not have to supply Dims for
// the common cases. Values sourced from the OpenAI documentation at
// https://platform.openai.com/docs/guides/embeddings/embedding-models.
func openAIModelDims(model string) int {
	switch model {
	case "text-embedding-3-small":
		return 1536
	case "text-embedding-3-large":
		return 3072
	case "text-embedding-ada-002":
		return 1536
	}
	return 0
}
