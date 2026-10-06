// Package bedrock re-exports the internal/llm/bedrock provider so
// external modules can run the agent loop against Amazon Bedrock
// without importing /internal.
package bedrock

import (
	"context"

	"github.com/sebastienrousseau/rousseau-agent/internal/llm/bedrock"
)

// Provider aliases [bedrock.Provider].
type Provider = bedrock.Provider

// Config aliases [bedrock.Config].
type Config = bedrock.Config

// New constructs a Provider. Alias for [bedrock.New].
func New(ctx context.Context, cfg Config) (*Provider, error) { return bedrock.New(ctx, cfg) }
