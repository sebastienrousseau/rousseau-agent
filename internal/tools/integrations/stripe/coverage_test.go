package stripe

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
	c, err := New(Config{SecretKey: "sk_test_x"})
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

func TestListChargesTool_HandlesEmptyInput(t *testing.T) {
	// Nil input is valid — every filter is optional, so the default
	// limit is applied and no customer filter is sent.
	srv, rec := newRecordingServer(t, http.StatusOK, `{"object":"list","data":[]}`)
	tool := NewListChargesTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "10", rec.Query.Get("limit"))
	assert.False(t, rec.Query.Has("customer"))
}

func TestListChargesTool_CustomerFilter(t *testing.T) {
	srv, rec := newRecordingServer(t, http.StatusOK, `{"object":"list","data":[]}`)
	tool := NewListChargesTool(newTestClient(t, srv))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"customer":"cus_123","limit":5}`))
	require.NoError(t, err)
	assert.Equal(t, "cus_123", rec.Query.Get("customer"))
	assert.Equal(t, "5", rec.Query.Get("limit"))
}
