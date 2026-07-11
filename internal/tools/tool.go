// Package tools defines the Tool contract and a Registry for discovery
// and lookup. Concrete built-in tools live in tools/builtin.
package tools

import (
	"context"
	"encoding/json"
)

// Definition describes a Tool as advertised to a Provider. It is the
// wire-shape variant of Tool, safe to hand across the provider boundary
// without also exporting the executable behaviour.
type Definition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// Tool is the contract every callable capability implements.
type Tool interface {
	// Name is the stable, unique identifier the model uses to call the
	// tool. Must match `^[a-z][a-z0-9_]*$`.
	Name() string
	// Description is the natural-language hint shown to the model.
	Description() string
	// InputSchema is a JSON Schema fragment describing valid input.
	// The map is a plain JSON Schema object, e.g.
	//   {"type": "object", "properties": {...}, "required": [...]}.
	InputSchema() map[string]any
	// Execute runs the tool. Implementations MUST honour ctx cancellation.
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}
