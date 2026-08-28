package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestBuildOpenAILike_MissingAPIKey(t *testing.T) {
	_, err := buildOpenAILike("openai", config.OpenAIConfig{Model: "gpt-4"})
	assert.Error(t, err)
}

func TestBuildOpenAILike_MissingModel(t *testing.T) {
	_, err := buildOpenAILike("openai", config.OpenAIConfig{APIKey: "sk-test"})
	assert.Error(t, err)
}

func TestBuildOpenAILike_HappyPath(t *testing.T) {
	p, err := buildOpenAILike("openrouter", config.OpenAIConfig{
		APIKey: "sk-test", Model: "gpt-4",
	})
	require.NoError(t, err)
	assert.Equal(t, "openrouter", p.Name())
}

func TestBuildProvider_OpenAI(t *testing.T) {
	p, err := buildProvider(&config.Config{
		Provider: "openai",
		OpenAI:   config.OpenAIConfig{APIKey: "sk-test", Model: "gpt-4"},
	})
	require.NoError(t, err)
	assert.Equal(t, "openai", p.Name())
}

func TestBuildProvider_OpenRouter(t *testing.T) {
	p, err := buildProvider(&config.Config{
		Provider:   "openrouter",
		OpenRouter: config.OpenAIConfig{APIKey: "sk-or", Model: "anthropic/claude-3.5-sonnet"},
	})
	require.NoError(t, err)
	assert.Equal(t, "openrouter", p.Name())
}

func TestBuildProvider_Ollama(t *testing.T) {
	p, err := buildProvider(&config.Config{
		Provider: "ollama",
		Ollama:   config.OpenAIConfig{APIKey: "x", Model: "qwen3-coder"},
	})
	require.NoError(t, err)
	assert.Equal(t, "ollama", p.Name())
}
