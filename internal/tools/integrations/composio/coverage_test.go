package composio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// TestListPage_WithCursor exercises the cursor branch.
func TestListPage_WithCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "abc123", r.URL.Query().Get("cursor"))
		writeBody(t, w, `{"items":[]}`)
	}))
	defer srv.Close()
	c, err := New(Config{APIKey: "k", UserID: "u", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	acts, err := c.ListPage(context.Background(), "abc123")
	require.NoError(t, err)
	assert.Empty(t, acts)
}

// TestAction_Description_UsesSpecDescription covers the branch
// where the spec provides a description.
func TestAction_Description_UsesSpecDescription(t *testing.T) {
	a := &action{spec: Action{Description: "explicit desc"}}
	assert.Equal(t, "explicit desc", a.Description())
}

// TestAction_InputSchema_MalformedFallsBack covers the JSON-parse
// error branch — returns a plain object schema.
func TestAction_InputSchema_MalformedFallsBack(t *testing.T) {
	a := &action{spec: Action{Parameters: json.RawMessage(`{not json`)}}
	schema := a.InputSchema()
	assert.Equal(t, "object", schema["type"])
}

// TestRegister_DuplicateActionsSkipped covers the duplicate-name
// branch — Register silently skips rather than failing.
func TestRegister_DuplicateActionsSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, `{"items":[
      {"name":"GMAIL_SEND","appKey":"gmail","parameters":{"type":"object"}},
      {"name":"GMAIL_SEND","appKey":"gmail","parameters":{"type":"object"}}
    ]}`)
	}))
	defer srv.Close()
	c, err := New(Config{APIKey: "k", UserID: "u", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)
	reg := tools.NewRegistry()
	n, err := Register(context.Background(), reg, c, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "duplicate registration silently dropped")
}

// TestClient_DoBuildRequestFailure surfaces the build-request error
// path (a cancelled ctx forces http.NewRequestWithContext to fail).
func TestClient_DoBuildRequestFailure(t *testing.T) {
	c, err := New(Config{APIKey: "k", UserID: "u", BaseURL: "http://x", HTTPClient: http.DefaultClient})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = c.do(ctx, http.MethodGet, "/actions", nil, nil)
	assert.Error(t, err)
}
