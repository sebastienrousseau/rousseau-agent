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
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/router"
	vertexllm "github.com/sebastienrousseau/rousseau-agent/internal/llm/vertex"
)

// buildProvider selects and constructs the LLM provider from Config.
// Callers should treat missing prerequisites (API key, binary) as
// user-facing errors and abort the command with the returned message.
func buildProvider(cfg *config.Config) (agent.Provider, error) {
	switch cfg.Provider {
	case "", "claudecli":
		extraArgs := cfg.ClaudeCLI.ExtraArgs
		if cfg.ClaudeCLI.Bare {
			// Prepend so operator ExtraArgs can still override or
			// re-enable specific bits if they need to.
			extraArgs = append([]string{"--bare"}, extraArgs...)
		}
		return claudecli.New(claudecli.Config{
			Binary:         cfg.ClaudeCLI.Binary,
			Model:          cfg.ClaudeCLI.Model,
			PermissionMode: cfg.ClaudeCLI.PermissionMode,
			ExtraArgs:      extraArgs,
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
	case "vertex":
		if cfg.Vertex.Project == "" || cfg.Vertex.Region == "" || cfg.Vertex.Model == "" {
			return nil, errors.New("provider=vertex requires vertex.{project, region, model}")
		}
		return vertexllm.New(context.Background(), vertexllm.Config{
			Project:         cfg.Vertex.Project,
			Region:          cfg.Vertex.Region,
			Model:           cfg.Vertex.Model,
			CredentialsFile: cfg.Vertex.CredentialsFile,
			MaxTokens:       cfg.Vertex.MaxTokens,
		})
	case "router":
		return buildRouter(cfg)
	default:
		return nil, fmt.Errorf("unknown provider %q (want claudecli/anthropic/openai/openrouter/ollama/bedrock/vertex/router)", cfg.Provider)
	}
}

// buildRouter constructs a [router.Router] from cfg.Router. Each named
// child under router.providers is itself built via buildChildProvider,
// which understands the same {kind, model, api_key, …} shape.
func buildRouter(cfg *config.Config) (agent.Provider, error) {
	rc := cfg.Router
	if rc.Default == "" {
		return nil, errors.New("provider=router but router.default is empty")
	}
	if len(rc.Providers) == 0 {
		return nil, errors.New("provider=router but router.providers is empty")
	}

	children := make(map[string]agent.Provider, len(rc.Providers))
	for name, childCfg := range rc.Providers {
		p, err := buildChildProvider(name, childCfg)
		if err != nil {
			return nil, fmt.Errorf("router.providers.%s: %w", name, err)
		}
		children[name] = p
	}

	rules := make([]router.Rule, 0, len(rc.Rules))
	for i, rule := range rc.Rules {
		rules = append(rules, router.Rule{
			Name:            rule.Name,
			MessageLenMax:   rule.MessageLenMax,
			MessageLenMin:   rule.MessageLenMin,
			ToolUseCountMax: rule.ToolUseCountMax,
			ToolUseCountMin: rule.ToolUseCountMin,
			SessionIDPrefix: rule.SessionIDPrefix,
			Use:             rule.Use,
		})
		_ = i
	}
	return router.New(router.Options{
		Default:   rc.Default,
		Rules:     rules,
		Providers: children,
	})
}

// buildChildProvider constructs one provider from a router-child config
// entry. The child config is a subset of the top-level provider config
// — enough to build one Anthropic / OpenAI / Bedrock / Vertex instance
// per child.
func buildChildProvider(name string, c config.RouterChildConfig) (agent.Provider, error) {
	switch c.Kind {
	case "anthropic":
		if c.APIKey == "" {
			return nil, fmt.Errorf("child %s: anthropic requires api_key", name)
		}
		return anthropic.New(anthropic.Config{
			APIKey:    c.APIKey,
			Model:     c.Model,
			MaxTokens: c.MaxTokens,
		})
	case "openai", "openrouter", "ollama":
		if c.APIKey == "" && c.Kind != "ollama" {
			return nil, fmt.Errorf("child %s: %s requires api_key", name, c.Kind)
		}
		return openaillm.New(openaillm.Config{
			APIKey:    c.APIKey,
			BaseURL:   c.BaseURL,
			Model:     c.Model,
			MaxTokens: c.MaxTokens,
			Name:      c.Kind,
		})
	case "bedrock":
		if c.Region == "" || c.Model == "" {
			return nil, fmt.Errorf("child %s: bedrock requires region+model", name)
		}
		return bedrockllm.New(context.Background(), bedrockllm.Config{
			Region:    c.Region,
			Model:     c.Model,
			Profile:   c.Profile,
			MaxTokens: c.MaxTokens,
		})
	case "vertex":
		if c.Project == "" || c.Region == "" || c.Model == "" {
			return nil, fmt.Errorf("child %s: vertex requires project+region+model", name)
		}
		return vertexllm.New(context.Background(), vertexllm.Config{
			Project:         c.Project,
			Region:          c.Region,
			Model:           c.Model,
			CredentialsFile: c.CredentialsFile,
			MaxTokens:       c.MaxTokens,
		})
	default:
		return nil, fmt.Errorf("child %s: unknown kind %q (want anthropic/openai/openrouter/ollama/bedrock/vertex)", name, c.Kind)
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
