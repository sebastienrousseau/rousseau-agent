package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

type nopProvider struct{}

func (nopProvider) Name() string                                            { return "nop" }
func (nopProvider) Complete(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{}, nil
}

func TestBuildCompressor_DisabledReturnsNoop(t *testing.T) {
	c := buildCompressor(config.CompressionConfig{Enabled: false}, nopProvider{})
	_, ok := c.(agent.NoopCompressor)
	assert.True(t, ok)
}

func TestBuildCompressor_NilProviderReturnsNoop(t *testing.T) {
	c := buildCompressor(config.CompressionConfig{Enabled: true}, nil)
	_, ok := c.(agent.NoopCompressor)
	assert.True(t, ok)
}

func TestBuildCompressor_EnabledReturnsLLM(t *testing.T) {
	c := buildCompressor(config.CompressionConfig{Enabled: true, TriggerMessages: 50, KeepRecent: 6}, nopProvider{})
	llm, ok := c.(*agent.LLMCompressor)
	assert.True(t, ok)
	assert.Equal(t, 50, llm.TriggerMessages)
	assert.Equal(t, 6, llm.KeepRecent)
}

func TestBuildCompressor_AppliesDefaults(t *testing.T) {
	c := buildCompressor(config.CompressionConfig{Enabled: true}, nopProvider{})
	llm, ok := c.(*agent.LLMCompressor)
	assert.True(t, ok)
	assert.Equal(t, 60, llm.TriggerMessages)
	assert.Equal(t, 8, llm.KeepRecent)
}
