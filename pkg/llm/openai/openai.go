// Package openai re-exports the internal/llm/openai provider so
// external modules can run the agent loop against OpenAI-compatible
// APIs (OpenAI, OpenRouter, Ollama, …) without importing /internal.
package openai

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/openai"
)

// Provider aliases [openai.Provider].
type Provider = openai.Provider

// Config aliases [openai.Config].
type Config = openai.Config

// New constructs a Provider. Alias for [openai.New].
func New(cfg Config) (*Provider, error) { return openai.New(cfg) }
