package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	mcpwire "github.com/sebastienrousseau/rousseau-agent/internal/mcp"
)

// These unit tests exercise Adapter branches that don't need a real
// MCP server subprocess: description formatting, schema fallback,
// content rendering, and the error-surface behaviour of Execute.
// Runs same-package so we can construct Adapter directly.

func TestAdapter_Name_HasPrefix(t *testing.T) {
	a := &Adapter{serverName: "github", toolName: "create_issue"}
	assert.Equal(t, "mcp:github:create_issue", a.Name())
}

func TestAdapter_Description_FallbackWhenEmpty(t *testing.T) {
	a := &Adapter{serverName: "github", toolName: "x", desc: ""}
	got := a.Description()
	assert.Contains(t, got, `"github"`)
	assert.Contains(t, got, "no description")
}

func TestAdapter_Description_PrefixesServerName(t *testing.T) {
	a := &Adapter{serverName: "playwright", desc: "browser automation"}
	got := a.Description()
	assert.Contains(t, got, `"playwright"`)
	assert.Contains(t, got, "browser automation")
}

func TestAdapter_InputSchema_FallbackOnEmpty(t *testing.T) {
	a := &Adapter{}
	schema := a.InputSchema()
	assert.Equal(t, "object", schema["type"])
	assert.Equal(t, true, schema["additionalProperties"])
}

func TestAdapter_InputSchema_FallbackOnMalformed(t *testing.T) {
	a := &Adapter{schema: json.RawMessage(`not-json`)}
	schema := a.InputSchema()
	assert.Equal(t, "object", schema["type"])
	assert.Equal(t, true, schema["additionalProperties"])
}

func TestAdapter_InputSchema_ForwardsValidJSON(t *testing.T) {
	a := &Adapter{schema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)}
	schema := a.InputSchema()
	assert.Equal(t, "object", schema["type"])
	assert.NotNil(t, schema["properties"])
}

func TestPermissiveSchema_Shape(t *testing.T) {
	s := permissiveSchema()
	assert.Equal(t, "object", s["type"])
	assert.Equal(t, true, s["additionalProperties"])
}

func TestRenderContent_TextBlocksJoined(t *testing.T) {
	cs := []mcpwire.Content{
		{Type: "text", Text: "line1"},
		{Type: "text", Text: "line2"},
	}
	got := renderContent(cs)
	assert.Equal(t, "line1\nline2", got)
}

func TestRenderContent_NonTextPlaceholder(t *testing.T) {
	cs := []mcpwire.Content{
		{Type: "text", Text: "prefix"},
		{Type: "image"},
	}
	got := renderContent(cs)
	assert.Contains(t, got, "prefix")
	assert.Contains(t, got, "[image content]")
}

func TestRenderContent_EmptyReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", renderContent(nil))
	assert.Equal(t, "", renderContent([]mcpwire.Content{}))
}

// Test-only: swap the *client.Client dependency with a callable
// interface via a private helper. Since Adapter references a
// concrete *client.Client, we test the Execute error-shape by
// constructing an Adapter whose client is nil — verifies the
// package handles nil gracefully by returning a Go panic-safe error.
func TestAdapter_ExecuteWithNilClientPanics(t *testing.T) {
	a := &Adapter{serverName: "x", toolName: "y", client: nil}
	// Calling Execute on a nil client would nil-deref; we don't
	// promise safety here (the constructor validates), but we do
	// promise the outer Register flow refuses nil clients.
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from nil-client Adapter.Execute")
		}
	}()
	_, _ = a.Execute(context.Background(), json.RawMessage(`{}`)) //nolint:errcheck // expects panic before return
}

// Guard against accidental import loops — this test only uses types
// from mcpwire, verifying the package compiles standalone.
var _ = errors.New
