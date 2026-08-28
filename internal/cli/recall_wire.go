package cli

import (
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/recall"
)

// buildEmbedder maps a RecallConfig to one of the three shipped
// recall.Embedder implementations (noop, voyage, openai) or reports
// an error the CLI can surface at startup. Kept as a pure factory
// so unit tests can exercise every branch without wiring a full
// daemon.
//
// An empty Embedder string is not an error — it returns (nil, nil)
// so the caller can distinguish "recall disabled" from
// "misconfigured". Nil is a valid input to every downstream that
// treats the embedder as optional.
func buildEmbedder(cfg config.RecallConfig) (recall.Embedder, error) {
	if !cfg.Enabled || cfg.Embedder == "" {
		return nil, nil
	}
	switch cfg.Embedder {
	case "noop":
		dims := cfg.EmbedderDims
		if dims == 0 {
			// NoopEmbedder's Dims() defaults to 4 on zero — same rule
			// applied here so the returned embedder self-reports the
			// same dimensionality the store will be sized against.
			dims = 4
		}
		return recall.NoopEmbedder{D: dims}, nil
	case "voyage":
		e, err := recall.NewVoyageEmbedder(recall.VoyageConfig{
			APIKey: cfg.EmbedderAPIKey,
			Model:  cfg.EmbedderModel,
			Dims:   cfg.EmbedderDims,
		})
		if err != nil {
			return nil, fmt.Errorf("recall.embedder=voyage: %w", err)
		}
		return e, nil
	case "openai":
		e, err := recall.NewOpenAIEmbedder(recall.OpenAIConfig{
			APIKey: cfg.EmbedderAPIKey,
			Model:  cfg.EmbedderModel,
			Dims:   cfg.EmbedderDims,
		})
		if err != nil {
			return nil, fmt.Errorf("recall.embedder=openai: %w", err)
		}
		return e, nil
	default:
		return nil, fmt.Errorf("recall.embedder=%q: unsupported (noop, voyage, openai)", cfg.Embedder)
	}
}
