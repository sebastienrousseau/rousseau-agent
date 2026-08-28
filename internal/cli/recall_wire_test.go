package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/recall"
)

func TestBuildEmbedder_DisabledReturnsNil(t *testing.T) {
	e, err := buildEmbedder(config.RecallConfig{Enabled: false, Embedder: "voyage"})
	require.NoError(t, err)
	assert.Nil(t, e, "disabled recall must return nil regardless of Embedder")
}

func TestBuildEmbedder_EnabledWithNoBackendReturnsNil(t *testing.T) {
	e, err := buildEmbedder(config.RecallConfig{Enabled: true, Embedder: ""})
	require.NoError(t, err)
	assert.Nil(t, e, "enabled but no Embedder set → nil, not an error")
}

func TestBuildEmbedder_NoopWithDefaultDims(t *testing.T) {
	e, err := buildEmbedder(config.RecallConfig{Enabled: true, Embedder: "noop"})
	require.NoError(t, err)
	require.NotNil(t, e)
	assert.Equal(t, "noop", e.Name())
	assert.Equal(t, 4, e.Dims(), "empty EmbedderDims defaults to 4")
}

func TestBuildEmbedder_NoopWithExplicitDims(t *testing.T) {
	e, err := buildEmbedder(config.RecallConfig{Enabled: true, Embedder: "noop", EmbedderDims: 8})
	require.NoError(t, err)
	require.NotNil(t, e)
	assert.Equal(t, 8, e.Dims())
}

func TestBuildEmbedder_VoyageBuildsWithExplicitKey(t *testing.T) {
	e, err := buildEmbedder(config.RecallConfig{
		Enabled:        true,
		Embedder:       "voyage",
		EmbedderAPIKey: "test-key",
	})
	require.NoError(t, err)
	require.NotNil(t, e)
	_, ok := e.(*recall.VoyageEmbedder)
	assert.True(t, ok)
	assert.Equal(t, "voyage:voyage-3-lite", e.Name())
}

func TestBuildEmbedder_VoyageWithoutKeyIsScoped(t *testing.T) {
	// Voyage has no OS fallback we can rely on being unset; force it.
	t.Setenv(recall.EnvVoyageAPIKey, "")
	_, err := buildEmbedder(config.RecallConfig{Enabled: true, Embedder: "voyage"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recall.embedder=voyage")
}

func TestBuildEmbedder_OpenAIBuildsWithExplicitKey(t *testing.T) {
	t.Setenv(recall.EnvOpenAIAPIKey, "")
	t.Setenv(recall.EnvOpenAIAPIKeyFallback, "")
	e, err := buildEmbedder(config.RecallConfig{
		Enabled:        true,
		Embedder:       "openai",
		EmbedderAPIKey: "test-key",
	})
	require.NoError(t, err)
	require.NotNil(t, e)
	_, ok := e.(*recall.OpenAIEmbedder)
	assert.True(t, ok)
	assert.Equal(t, "openai:text-embedding-3-small", e.Name())
	assert.Equal(t, 1536, e.Dims())
}

func TestBuildEmbedder_OpenAIWithoutKeyIsScoped(t *testing.T) {
	t.Setenv(recall.EnvOpenAIAPIKey, "")
	t.Setenv(recall.EnvOpenAIAPIKeyFallback, "")
	_, err := buildEmbedder(config.RecallConfig{Enabled: true, Embedder: "openai"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recall.embedder=openai")
}

func TestBuildEmbedder_UnsupportedBackendErrors(t *testing.T) {
	_, err := buildEmbedder(config.RecallConfig{Enabled: true, Embedder: "sentence-transformers"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
	assert.Contains(t, err.Error(), "sentence-transformers")
}

func TestBuildEmbedder_OpenAIWithExplicitModelAndDims(t *testing.T) {
	t.Setenv(recall.EnvOpenAIAPIKey, "")
	t.Setenv(recall.EnvOpenAIAPIKeyFallback, "")
	e, err := buildEmbedder(config.RecallConfig{
		Enabled:        true,
		Embedder:       "openai",
		EmbedderAPIKey: "test",
		EmbedderModel:  "text-embedding-3-large",
		EmbedderDims:   3072,
	})
	require.NoError(t, err)
	assert.Equal(t, 3072, e.Dims())
	assert.Equal(t, "openai:text-embedding-3-large", e.Name())
}
