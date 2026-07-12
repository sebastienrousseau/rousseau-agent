package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTool struct{ n string }

func (f *fakeTool) Name() string                { return f.n }
func (f *fakeTool) Description() string         { return "fake" }
func (f *fakeTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (f *fakeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&fakeTool{n: "a"}))
	got, ok := r.Get("a")
	require.True(t, ok)
	assert.Equal(t, "a", got.Name())
}

func TestRegistry_DuplicateName(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&fakeTool{n: "a"}))
	assert.Error(t, r.Register(&fakeTool{n: "a"}))
}

func TestRegistry_Definitions(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&fakeTool{n: "a"}))
	require.NoError(t, r.Register(&fakeTool{n: "b"}))
	defs := r.Definitions()
	assert.Len(t, defs, 2)
}

func TestRegistry_MissingLookup(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Get("missing")
	assert.False(t, ok)
}

func TestRegistry_Names(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&fakeTool{n: "a"}))
	require.NoError(t, r.Register(&fakeTool{n: "b"}))
	names := r.Names()
	assert.Len(t, names, 2)
	assert.Contains(t, names, "a")
	assert.Contains(t, names, "b")
}

func TestRegistry_MustRegister(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(&fakeTool{n: "a"})
	got, ok := r.Get("a")
	require.True(t, ok)
	assert.Equal(t, "a", got.Name())
}

func TestRegistry_MustRegister_PanicsOnDuplicate(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(&fakeTool{n: "a"})
	assert.Panics(t, func() { r.MustRegister(&fakeTool{n: "a"}) })
}
