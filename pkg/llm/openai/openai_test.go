package openai_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgagent "github.com/sebastienrousseau/rousseau-agent/pkg/agent"
	"github.com/sebastienrousseau/rousseau-agent/pkg/llm/openai"
)

var _ pkgagent.Provider = (*openai.Provider)(nil)

func TestNew(t *testing.T) {
	p, err := openai.New(openai.Config{APIKey: "sk-test", Model: "gpt-5"})
	require.NoError(t, err)
	assert.NotEmpty(t, p.Name())

	_, err = openai.New(openai.Config{})
	assert.Error(t, err, "missing API key is rejected")
}
