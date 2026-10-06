// Package anthropic re-exports the internal/llm/anthropic provider so
// external modules can run the agent loop against the Anthropic API
// without importing /internal.
package anthropic

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/anthropic"
)

// Provider aliases [anthropic.Provider]. It implements both
// pkg/agent.Provider and pkg/agent.StreamingProvider.
type Provider = anthropic.Provider

// Config aliases [anthropic.Config].
type Config = anthropic.Config

// New constructs a Provider. Alias for [anthropic.New].
func New(cfg Config) (*Provider, error) { return anthropic.New(cfg) }
