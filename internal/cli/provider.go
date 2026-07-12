package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/anthropic"
	bedrockllm "github.com/sebastienrousseau/rousseau-agent/internal/llm/bedrock"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/claudecli"
	openaillm "github.com/sebastienrousseau/rousseau-agent/internal/llm/openai"
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
	case "openai":
		return buildOpenAILike("openai", cfg.OpenAI)
	case "openrouter":
		return buildOpenAILike("openrouter", cfg.OpenRouter)
	case "ollama":
		return buildOpenAILike("ollama", cfg.Ollama)
	case "bedrock":
		if cfg.Bedrock.Region == "" {
			return nil, errors.New("provider=bedrock but bedrock.region is empty")
		}
		if cfg.Bedrock.Model == "" {
			return nil, errors.New("provider=bedrock but bedrock.model is empty")
		}
		return bedrockllm.New(context.Background(), bedrockllm.Config{
			Region:    cfg.Bedrock.Region,
			Model:     cfg.Bedrock.Model,
			Profile:   cfg.Bedrock.Profile,
			MaxTokens: cfg.Bedrock.MaxTokens,
		})
	default:
		return nil, fmt.Errorf("unknown provider %q (want claudecli/anthropic/openai/openrouter/ollama)", cfg.Provider)
	}
}

func buildOpenAILike(name string, c config.OpenAIConfig) (agent.Provider, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("provider=%s but api_key is empty", name)
	}
	if c.Model == "" {
		return nil, fmt.Errorf("provider=%s but model is empty (there is no universal default)", name)
	}
	return openaillm.New(openaillm.Config{
		APIKey:    c.APIKey,
		BaseURL:   c.BaseURL,
		Model:     c.Model,
		MaxTokens: c.MaxTokens,
		Name:      name,
	})
}
