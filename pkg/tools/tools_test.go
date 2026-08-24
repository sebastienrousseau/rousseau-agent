package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgtools "github.com/sebastienrousseau/rousseau-agent/pkg/tools"
)

// noopTool satisfies pkgtools.Tool via the internal Tool interface
// aliasing — exercises that the façade's type alias is a true alias
// (not a new named type) so implementations in external modules
// remain assignable.
type noopTool struct{}

func (*noopTool) Name() string             { return "noop" }
func (*noopTool) Description() string      { return "does nothing" }
func (*noopTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (*noopTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "ok", nil
}

func TestNewRegistry_ReturnsEmpty(t *testing.T) {
	r := pkgtools.NewRegistry()
	require.NotNil(t, r)
	assert.Empty(t, r.Names())
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := pkgtools.NewRegistry()
	tool := pkgtools.Tool(&noopTool{})
	require.NoError(t, r.Register(tool))

	got, ok := r.Get("noop")
	assert.True(t, ok)
	assert.Equal(t, "noop", got.Name())
}

func TestRegistry_Definitions(t *testing.T) {
	r := pkgtools.NewRegistry()
	require.NoError(t, r.Register(&noopTool{}))
	defs := r.Definitions()
	require.Len(t, defs, 1)
	assert.Equal(t, "noop", defs[0].Name)
	assert.Equal(t, "does nothing", defs[0].Description)
}

func TestDefinition_TypeAlias(t *testing.T) {
	// A composite-literal Definition constructed via the pkg alias
	// must be assignable — verifies the alias is transparent.
	d := pkgtools.Definition{Name: "x", Description: "y", InputSchema: map[string]any{}}
	assert.Equal(t, "x", d.Name)
}
