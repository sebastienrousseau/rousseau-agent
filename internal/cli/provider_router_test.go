package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestBuildRouter_MissingDefault(t *testing.T) {
	_, err := buildRouter(&config.Config{
		Router: config.RouterConfig{
			Providers: map[string]config.RouterChildConfig{
				"any": {Kind: "anthropic", APIKey: "sk", Model: "x"},
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "router.default")
}

func TestBuildRouter_MissingProviders(t *testing.T) {
	_, err := buildRouter(&config.Config{
		Router: config.RouterConfig{Default: "haiku"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "router.providers")
}

func TestBuildRouter_ChildBuildErrorSurfaces(t *testing.T) {
	_, err := buildRouter(&config.Config{
		Router: config.RouterConfig{
			Default: "haiku",
			Providers: map[string]config.RouterChildConfig{
				"haiku": {Kind: "anthropic"}, // missing api_key
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "router.providers.haiku")
	assert.Contains(t, err.Error(), "api_key")
}

func TestBuildRouter_HappyPathAppliesRules(t *testing.T) {
	p, err := buildRouter(&config.Config{
		Router: config.RouterConfig{
			Default: "sonnet",
			Providers: map[string]config.RouterChildConfig{
				"sonnet": {Kind: "openai", APIKey: "sk", Model: "gpt-4"},
				"haiku":  {Kind: "openai", APIKey: "sk", Model: "gpt-4o-mini"},
			},
			Rules: []config.RouterRuleConfig{
				{Name: "cheap", MessageLenMax: 100, Use: "haiku"},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "router", p.Name())
}

func TestBuildChildProvider_UnknownKindErrors(t *testing.T) {
	_, err := buildChildProvider("x", config.RouterChildConfig{Kind: "brain"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown kind")
}

func TestBuildChildProvider_AnthropicMissingKey(t *testing.T) {
	_, err := buildChildProvider("a", config.RouterChildConfig{Kind: "anthropic"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "anthropic")
}

func TestBuildChildProvider_AnthropicHappy(t *testing.T) {
	p, err := buildChildProvider("a", config.RouterChildConfig{
		Kind: "anthropic", APIKey: "sk", Model: "claude-3-5",
	})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", p.Name())
}

func TestBuildChildProvider_OpenAIMissingKey(t *testing.T) {
	_, err := buildChildProvider("o", config.RouterChildConfig{Kind: "openai"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api_key")
}

func TestBuildChildProvider_OpenAIHappy(t *testing.T) {
	p, err := buildChildProvider("o", config.RouterChildConfig{
		Kind: "openai", APIKey: "sk", Model: "gpt-4",
	})
	require.NoError(t, err)
	assert.Equal(t, "openai", p.Name())
}

func TestBuildChildProvider_OllamaAcceptsDummyKey(t *testing.T) {
	// buildChildProvider skips the api_key check for Kind=ollama;
	// the underlying openaillm.New still needs a non-empty key
	// (typically "x" for local ollama).
	p, err := buildChildProvider("o", config.RouterChildConfig{
		Kind: "ollama", APIKey: "x", Model: "qwen3",
	})
	require.NoError(t, err)
	assert.Equal(t, "ollama", p.Name())
}

func TestBuildChildProvider_BedrockMissingRegion(t *testing.T) {
	_, err := buildChildProvider("b", config.RouterChildConfig{Kind: "bedrock", Model: "anthropic.claude"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "region")
}

func TestBuildChildProvider_VertexMissingProject(t *testing.T) {
	_, err := buildChildProvider("v", config.RouterChildConfig{
		Kind: "vertex", Region: "us-central1", Model: "claude",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project")
}

func TestBuildChildProvider_ErrorMentionsChildName(t *testing.T) {
	_, err := buildChildProvider("weird-name-42", config.RouterChildConfig{Kind: "nope"})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "weird-name-42"),
		"error should include the child name for operator debugging")
}
