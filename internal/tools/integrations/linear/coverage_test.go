package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func TestEveryToolExposesMetadata(t *testing.T) {
	c, err := New(Config{APIKey: "lin_api_x"})
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

func TestGetIssueTool_ValidatesInput(t *testing.T) {
	tool := NewGetIssueTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.ErrorContains(t, err, "required")
}

func TestUpdateIssueTool_ValidatesInput(t *testing.T) {
	tool := NewUpdateIssueTool(&Client{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"title":"new"}`))
	assert.ErrorContains(t, err, "required")
}

func TestListIssuesTool_HandlesEmptyInput(t *testing.T) {
	// Nil input JSON is allowed — every filter is optional, so the tool
	// must still issue a well-formed GraphQL query.
	srv, rec := newGQLServer(t, http.StatusOK, `{"data":{"issues":{"nodes":[]}}}`)
	tool := NewListIssuesTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Contains(t, rec.Query, "issues(filter")
}
