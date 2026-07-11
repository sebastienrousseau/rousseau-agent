package cli

import (
	"errors"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/anthropic"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/claudecli"
)

// buildProvider selects and constructs the LLM provider from Config.
// Callers should treat missing prerequisites (API key, binary) as
// user-facing errors and abort the command with the returned message.
func buildProvider(cfg *config.Config) (agent.Provider, error) {
	switch cfg.Provider {
	case "", "claudecli":
		return claudecli.New(claudecli.Config{
			Binary:         cfg.ClaudeCLI.Binary,
			Model:          cfg.ClaudeCLI.Model,
			PermissionMode: cfg.ClaudeCLI.PermissionMode,
			ExtraArgs:      cfg.ClaudeCLI.ExtraArgs,
		}), nil
	case "anthropic":
		if cfg.Anthropic.APIKey == "" {
			return nil, errors.New("provider=anthropic but ANTHROPIC_API_KEY is not set (env var or anthropic.api_key in config)")
		}
		return anthropic.New(anthropic.Config{
			APIKey:    cfg.Anthropic.APIKey,
			Model:     cfg.Anthropic.Model,
			MaxTokens: cfg.Anthropic.MaxTokens,
		})
	default:
		return nil, fmt.Errorf("unknown provider %q (want claudecli or anthropic)", cfg.Provider)
	}
}
