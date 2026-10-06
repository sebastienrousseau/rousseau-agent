package llm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgagent "github.com/sebastienrousseau/rousseau-agent/pkg/agent"
	"github.com/sebastienrousseau/rousseau-agent/pkg/llm/anthropic"
	"github.com/sebastienrousseau/rousseau-agent/pkg/llm/bedrock"
	"github.com/sebastienrousseau/rousseau-agent/pkg/llm/openai"
	"github.com/sebastienrousseau/rousseau-agent/pkg/llm/vertex"
)

// Every promoted provider satisfies the public Provider contract, and
// the streaming-capable one also satisfies StreamingProvider, so an
// external module can pick any of them for pkg/agent.New.
var (
	_ pkgagent.Provider          = (*anthropic.Provider)(nil)
	_ pkgagent.StreamingProvider = (*anthropic.Provider)(nil)
	_ pkgagent.Provider          = (*openai.Provider)(nil)
	_ pkgagent.Provider          = (*bedrock.Provider)(nil)
	_ pkgagent.Provider          = (*vertex.Provider)(nil)
)

func TestAnthropicFacade_ConstructsWithKey(t *testing.T) {
	p, err := anthropic.New(anthropic.Config{APIKey: "sk-test", Model: "claude-sonnet-4-6"})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", p.Name())
}

func TestOpenAIFacade_ConstructsWithKey(t *testing.T) {
	p, err := openai.New(openai.Config{APIKey: "sk-test", Model: "gpt-5"})
	require.NoError(t, err)
	assert.NotEmpty(t, p.Name())
}

func TestFacades_RejectEmptyConfig(t *testing.T) {
	_, err := anthropic.New(anthropic.Config{})
	assert.Error(t, err)
	_, err = openai.New(openai.Config{})
	assert.Error(t, err)
}
