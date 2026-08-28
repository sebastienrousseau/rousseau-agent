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

// VoyageEmbedder calls the Voyage API's `/embeddings` endpoint. Default
// model is voyage-3-lite (1024 dims) — the cheapest quality tier
// Anthropic recommends for Claude-shaped retrieval.
type VoyageEmbedder struct {
	apiKey  string
	model   string
	dims    int
	http    *http.Client
	baseURL string
}

// VoyageConfig configures a VoyageEmbedder.
type VoyageConfig struct {
	// APIKey — Voyage API key. Empty falls back to
	// $ROUSSEAU_VOYAGE_API_KEY.
	APIKey string
	// Model — one of Voyage's supported model IDs. Empty uses
	// "voyage-3-lite".
	Model string
	// Dims — the model's vector dimensionality. Required unless Model
	// is one of the well-known defaults handled by voyageModelDims.
	Dims int
	// BaseURL — override for tests. Empty uses
	// https://api.voyageai.com/v1.
	BaseURL string
	// HTTPClient — injected in tests. Empty uses a 30s-timeout client.
	HTTPClient *http.Client
}

// EnvVoyageAPIKey is the env fallback for VoyageConfig.APIKey.
const EnvVoyageAPIKey = "ROUSSEAU_VOYAGE_API_KEY"

// NewVoyageEmbedder constructs a Voyage-backed embedder.
func NewVoyageEmbedder(cfg VoyageConfig) (*VoyageEmbedder, error) {
	key := cfg.APIKey
	if key == "" {
		key = os.Getenv(EnvVoyageAPIKey)
	}
	if key == "" {
		return nil, fmt.Errorf("voyage: API key required (set $%s or Config.APIKey)", EnvVoyageAPIKey)
	}
	model := cfg.Model
	if model == "" {
		model = "voyage-3-lite"
	}
	dims := cfg.Dims
	if dims == 0 {
		dims = voyageModelDims(model)
	}
	if dims == 0 {
		return nil, fmt.Errorf("voyage: unknown model %q — supply Dims explicitly", model)
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.voyageai.com/v1"
	}
	h := cfg.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	return &VoyageEmbedder{apiKey: key, model: model, dims: dims, http: h, baseURL: base}, nil
}

// Embed satisfies Embedder.
func (v *VoyageEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{"input": texts, "model": v.model})
	if err != nil {
		return nil, fmt.Errorf("voyage: marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("voyage: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+v.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024)) //nolint:errcheck // best-effort read of error body
		return nil, fmt.Errorf("voyage: HTTP %d: %s", resp.StatusCode, string(snippet))
	}

	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("voyage: decode response: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("voyage: expected %d embeddings, got %d", len(texts), len(out.Data))
	}
	vecs := make([][]float32, len(texts))
	for _, row := range out.Data {
		if row.Index < 0 || row.Index >= len(vecs) {
			return nil, fmt.Errorf("voyage: bad response index %d", row.Index)
		}
		vecs[row.Index] = row.Embedding
	}
	return vecs, nil
}

// Dims satisfies Embedder.
func (v *VoyageEmbedder) Dims() int { return v.dims }

// Name satisfies Embedder.
func (v *VoyageEmbedder) Name() string { return "voyage:" + v.model }

// voyageModelDims maps well-known Voyage model IDs to their vector
// dimensionality so operators don't have to supply Dims for the
// common cases.
func voyageModelDims(model string) int {
	switch model {
	case "voyage-3-lite":
		return 1024
	case "voyage-3":
		return 1024
	case "voyage-3-large":
		return 1024
	case "voyage-code-3":
		return 1024
	}
	return 0
}
