// Package claudecli re-exports the internal/llm/claudecli provider
// so external modules can use it without importing /internal.
package claudecli

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/claudecli"
)

// Provider aliases [claudecli.Provider].
type Provider = claudecli.Provider

// Config aliases [claudecli.Config].
type Config = claudecli.Config

// New constructs a Provider. Alias for [claudecli.New].
func New(cfg Config) *Provider { return claudecli.New(cfg) }
