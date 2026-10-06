package anthropic_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgagent "github.com/sebastienrousseau/rousseau-agent/pkg/agent"
	"github.com/sebastienrousseau/rousseau-agent/pkg/llm/anthropic"
)

var (
	_ pkgagent.Provider          = (*anthropic.Provider)(nil)
	_ pkgagent.StreamingProvider = (*anthropic.Provider)(nil)
)

func TestNew(t *testing.T) {
	p, err := anthropic.New(anthropic.Config{APIKey: "sk-test", Model: "claude-sonnet-4-6"})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", p.Name())

	_, err = anthropic.New(anthropic.Config{})
	assert.Error(t, err, "missing API key is rejected")
}
