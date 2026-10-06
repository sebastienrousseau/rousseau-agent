// Package vertex re-exports the internal/llm/vertex provider so
// external modules can run the agent loop against Google Vertex AI
// without importing /internal.
package vertex

import (
	"context"

	"github.com/sebastienrousseau/rousseau-agent/internal/llm/vertex"
)

// Provider aliases [vertex.Provider].
type Provider = vertex.Provider

// Config aliases [vertex.Config].
type Config = vertex.Config

// New constructs a Provider. Alias for [vertex.New].
func New(ctx context.Context, cfg Config) (*Provider, error) { return vertex.New(ctx, cfg) }
