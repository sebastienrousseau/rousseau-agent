package google

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func TestEveryToolExposesMetadata(t *testing.T) {
	c, err := New(Config{AccessToken: "x"})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	require.NoError(t, Register(reg, c))
	for _, name := range reg.Names() {
		tool, ok := reg.Get(name)
		require.True(t, ok)
		assert.NotEmpty(t, tool.Description())
		schema := tool.InputSchema()
		assert.Equal(t, "object", schema["type"])
	}
}

func TestGmailGetTool_ValidatesInput(t *testing.T) {
	tool := NewGmailGetTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.ErrorContains(t, err, "required")
}

func TestDriveGetTool_ValidatesInput(t *testing.T) {
	tool := NewDriveGetTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.ErrorContains(t, err, "required")
}

func TestDriveSearchTool_ValidatesInput(t *testing.T) {
	tool := NewDriveSearchTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.ErrorContains(t, err, "required")
}

func TestCalendarCreateEventTool_ValidatesInput(t *testing.T) {
	tool := NewCalendarCreateEventTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"summary":"m"}`))
	assert.ErrorContains(t, err, "required")
}

func TestGmailListTool_HandlesEmptyInput(t *testing.T) {
	c, err := New(Config{AccessToken: "x"})
	require.NoError(t, err)
	tool := NewGmailListTool(c)
	// Empty input JSON is valid — Gmail's q is optional.
	_, err = tool.Execute(context.Background(), nil)
	if err != nil {
		assert.NotContains(t, err.Error(), "bad input")
	}
}

func TestBuildRFC5322_Roundtrip(t *testing.T) {
	got := buildRFC5322("bot@x", "user@y", "subj", "body")
	assert.Contains(t, string(got), "From: bot@x")
	assert.Contains(t, string(got), "To: user@y")
	assert.Contains(t, string(got), "Subject: subj")
	assert.Contains(t, string(got), "body")
}

func TestBuildRFC5322_NoFrom(t *testing.T) {
	// From is optional; when Gmail sends its default From: header is
	// injected server-side.
	got := buildRFC5322("", "user@y", "subj", "body")
	assert.NotContains(t, string(got), "From: ")
	assert.Contains(t, string(got), "To: user@y")
}
